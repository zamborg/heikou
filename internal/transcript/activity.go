package transcript

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zamborg/heikou/internal/format"
	"github.com/zamborg/heikou/internal/heikou"
)

const (
	// activityTailBytes is how much of the end of a transcript the activity
	// reader looks at. It is a tail rather than a whole file because this runs
	// on a timer for every live session: a transcript grows without limit, and
	// what a session is doing right now is always in its last few records.
	//
	// The size is chosen against the records that are actually there. A tool
	// call is a few hundred bytes; a tool result carrying a file can be a
	// megabyte, and one of those fills the window on its own. When that happens
	// the reader reports nothing rather than guessing, and the caller falls
	// through to whatever it would have shown anyway.
	activityTailBytes = 128 << 10
	// activityDetailRunes bounds what one activity says. A command or a path is
	// written by the model and lands in a terminal, so it is untrusted text and
	// needs a ceiling that is not the width of whatever draws it.
	activityDetailRunes = 200
)

// ActivityKind is what a runner's own record says was happening when it was
// last written.
//
// It is deliberately not the agent turn state in
// todos/session-status-titles.md. That is a claim about now, made for the
// status column, and it needs a signal that reports the runner's current state
// rather than its most recent record. This is a reading of the last record, and
// nothing here should be promoted into that column.
type ActivityKind string

const (
	// ActivityUnknown means the window held nothing that says what is going on.
	// It is the zero value, so a caller that ignores Availability still gets
	// the cautious answer.
	ActivityUnknown ActivityKind = ""
	// ActivityRunning means the last record is a tool call. The call may be
	// executing or waiting for the user to approve it; the transcript does not
	// distinguish those, so neither does this.
	ActivityRunning ActivityKind = "running"
	// ActivityWorking means the last record is a finished tool result, or a
	// message to the model with no reply after it. Either way the runner has
	// been given something and has not recorded an answer.
	ActivityWorking ActivityKind = "working"
	// ActivityReplied means the model's turn ended.
	ActivityReplied ActivityKind = "replied"
)

// Activity is the most recent thing a runner recorded a session doing.
//
// It reports what is in the file and no interpretation of it: which tool was
// called, what the call named, what a finished reply said. Turning that into a
// phrase belongs to whoever is drawing, because the words differ between a row
// with twenty columns and a pane with a whole line.
type Activity struct {
	SessionID    string         `json:"session_id"`
	Runner       heikou.Backend `json:"runner"`
	Availability Availability   `json:"availability"`
	// Reason explains anything other than Available, in one line.
	Reason string `json:"reason,omitempty"`
	// Path is where the records were read from, and is empty unless Available.
	Path string       `json:"path,omitempty"`
	Kind ActivityKind `json:"kind,omitempty"`
	// Tool is the tool being run, named as the runner named it, and is set only
	// when Kind is ActivityRunning.
	Tool string `json:"tool,omitempty"`
	// Detail is what the record was about: the path a tool acted on, the command
	// it ran, or the first line of a finished reply. It is sanitized and
	// bounded, and it is empty when the record named no subject.
	Detail string `json:"detail,omitempty"`
	// At is the timestamp of the record this was read from.
	At time.Time `json:"at,omitzero"`
}

// Known reports whether the reader found a record it could interpret. A located
// transcript whose tail says nothing is not knowledge: there is no activity to
// show, and a caller should behave as though it had asked nothing.
func (a Activity) Known() bool { return a.Availability == Available && a.Kind != ActivityUnknown }

// Subject is Detail with a path reduced to its base name, which is what fits in
// a row. It lives here rather than in a renderer because deciding whether a
// tool's subject is a path needs the tool's name, which this package owns.
func (a Activity) Subject() string {
	switch a.Tool {
	case "Read", "Edit", "Write", "MultiEdit", "NotebookEdit", "NotebookRead":
		if a.Detail == "" {
			return ""
		}
		return filepath.Base(a.Detail)
	default:
		return a.Detail
	}
}

// ReadActivity returns what the runner last recorded this session doing.
//
// Like Read, an absent transcript is a successful answer rather than an error,
// and an error is reserved for a filesystem that misbehaved. Unlike Read it
// never scans the whole file: it seeks to the end and interprets one window,
// because it runs on a timer for every live session on screen.
func (r Reader) ReadActivity(request Request) (Activity, error) {
	result := Activity{SessionID: request.SessionID, Runner: request.Runner}
	if strings.TrimSpace(request.SessionID) == "" {
		return result, errors.New("read activity: session id is empty")
	}
	if request.Runner != heikou.BackendClaude {
		result.Availability = Unsupported
		result.Reason = unsupportedReason(request.Runner)
		return result, nil
	}

	path, err := r.locateClaude(request.SessionID, request.Root)
	if err != nil {
		return result, err
	}
	if path == "" {
		result.Availability = Missing
		result.Reason = fmt.Sprintf("no transcript for this session under %s", format.CompactPath(r.claudeProjects()))
		return result, nil
	}

	lines, err := readTail(path, activityTailBytes)
	if err != nil {
		return result, err
	}
	result.Availability = Available
	result.Path = path
	interpretActivity(lines, &result)
	return result, nil
}

// readTail returns the complete lines at the end of a file.
//
// Two partial records are expected here and neither one is damage. The first
// line of the window is whatever the seek landed in the middle of, so it is
// dropped outright. The last line may be one the runner is appending right now,
// and it is left to fail decoding like any other unreadable record.
func readTail(path string, window int64) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// The runner removed the transcript between locating and opening it.
			// That is a race with another program's files, not a failure here.
			return nil, nil
		}
		return nil, fmt.Errorf("open transcript %s: %w", format.CompactPath(path), err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat transcript %s: %w", format.CompactPath(path), err)
	}
	offset := max(info.Size()-window, 0)
	data := make([]byte, window)
	read, err := file.ReadAt(data, offset)
	if read == 0 {
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read transcript %s: %w", format.CompactPath(path), err)
		}
		return nil, nil
	}

	lines := strings.Split(string(data[:read]), "\n")
	if offset > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	return lines, nil
}

// interpretActivity keeps the last record in the window that says anything, and
// reports that. Later records win outright: a tool result always follows the
// call it answers, so "the last thing written" and "the thing still in flight"
// are the same reading.
func interpretActivity(lines []string, result *Activity) {
	var latest Activity
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		record, ok := decodeRecord([]byte(line))
		if !ok || record.IsSidechain || record.IsMeta || record.IsCompactSummary || record.Message == nil {
			continue
		}
		texts, blocks, onlyToolResults := activityBlocks(record.Message.Content)
		switch record.Type {
		case "assistant":
			for _, block := range blocks {
				if block.Type == "tool_use" && block.Name != "" {
					latest = Activity{
						Kind: ActivityRunning, Tool: block.Name,
						Detail: toolSubject(block.Name, block.Input), At: record.Timestamp,
					}
				}
			}
			// A reply that stopped for any reason other than another tool call
			// is finished. Only "tool_use" promises more records; an absent stop
			// reason promises nothing and is not read as an ending.
			if reason := record.Message.StopReason; reason != "" && reason != "tool_use" {
				latest = Activity{Kind: ActivityReplied, Detail: firstBounded(texts), At: record.Timestamp}
			}
		case "user":
			if onlyToolResults {
				latest = Activity{Kind: ActivityWorking, At: record.Timestamp}
				continue
			}
			if len(texts) == 0 {
				continue
			}
			// A local slash command's output is the transcript's equivalent of a
			// tool result and says nothing about the runner's state.
			if _, drop, matched := localCommandEnvelope(texts[0]); matched && drop {
				continue
			}
			latest = Activity{Kind: ActivityWorking, At: record.Timestamp}
		}
	}
	result.Kind, result.Tool, result.Detail, result.At = latest.Kind, latest.Tool, latest.Detail, latest.At
}

// activityBlocks reads a message body the way decodeContent does, but keeps the
// blocks themselves: saying what a call is about needs its input, which a turn
// never looks at.
func activityBlocks(raw json.RawMessage) (texts []string, blocks []contentBlock, onlyToolResults bool) {
	if len(raw) == 0 {
		return nil, nil, false
	}
	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil {
		if trimmed := strings.TrimSpace(plain); trimmed != "" {
			texts = append(texts, trimmed)
		}
		return texts, nil, false
	}
	if err := json.Unmarshal(raw, &blocks); err != nil || len(blocks) == 0 {
		return nil, nil, false
	}
	onlyToolResults = true
	for _, block := range blocks {
		if block.Type == "tool_result" {
			continue
		}
		if block.Type == "text" {
			if trimmed := strings.TrimSpace(block.Text); trimmed != "" {
				texts = append(texts, trimmed)
			}
		}
		onlyToolResults = false
	}
	return texts, blocks, onlyToolResults
}

func firstBounded(texts []string) string {
	for _, text := range texts {
		if bounded := boundActivity(text); bounded != "" {
			return bounded
		}
	}
	return ""
}

// toolInput is every field this package reads out of a tool call. The decoder
// ignores the rest, so a tool whose input shape nobody here knows contributes
// its name and no subject rather than failing.
type toolInput struct {
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
	Command      string `json:"command"`
	Pattern      string `json:"pattern"`
	URL          string `json:"url"`
	Query        string `json:"query"`
	Description  string `json:"description"`
	Path         string `json:"path"`
}

// toolSubject returns what a call names, chosen per tool because only the tool
// knows which of its arguments is the thing being acted on.
//
// Bash reports its command rather than the description beside it. The
// description is prose the model wrote about what it means to do; the command
// is what will actually run, it is what someone watching a dashboard wants to
// read, and it cannot disagree with the process.
func toolSubject(name string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var fields toolInput
	if err := json.Unmarshal(input, &fields); err != nil {
		return ""
	}
	switch name {
	case "Bash", "BashOutput", "KillShell":
		return boundActivity(fields.Command)
	case "Read", "Edit", "Write", "MultiEdit":
		return boundActivity(fields.FilePath)
	case "NotebookEdit", "NotebookRead":
		return boundActivity(firstNonEmpty(fields.NotebookPath, fields.FilePath))
	case "Grep", "Glob":
		return boundActivity(fields.Pattern)
	case "WebFetch":
		return boundActivity(fields.URL)
	case "WebSearch":
		return boundActivity(fields.Query)
	case "Task", "Agent", "Skill":
		return boundActivity(fields.Description)
	default:
		return boundActivity(firstNonEmpty(fields.Description, fields.FilePath, fields.Path, fields.Command))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func boundActivity(value string) string {
	value = format.OneLine(value)
	if utf8.RuneCountInString(value) <= activityDetailRunes {
		return value
	}
	return truncateRunes(value, activityDetailRunes)
}
