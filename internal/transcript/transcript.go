// Package transcript reads what a native runner recorded about a session.
//
// It exists because `h peek` cannot answer the most common question about a
// session. A full-screen runner draws on the terminal's alternate screen, which
// keeps no scrollback, so a capture returns the pane's current frame and
// nothing that scrolled past it. That is a property of the runtime, not a
// missing flag: no capture depth recovers what was never retained.
//
// So history cannot come from tmux. It has to come from the runner, and this
// package is the observer that reads it — read-only, bounded, and failing soft.
// A missing transcript is a normal answer, because the file layout belongs to
// the runner and may change without notice. Nothing here is a claim about
// whether a session is alive, healthy, or waiting; the runtime state enum
// remains the only claim Heikou makes about now.
//
// The package is a leaf above the domain types. It never touches Heikou state,
// never copies a transcript into it, and never writes.
package transcript

import (
	"bufio"
	"bytes"
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
	// DefaultTurns is how many recent turns a caller gets when it does not say.
	// It is a reading length, not a limit on what exists: TotalTurns always
	// reports the whole transcript.
	DefaultTurns = 20

	// maxTranscriptBytes bounds one read. Transcripts grow without limit and
	// are owned by another program, so the cap is what keeps a runaway file
	// from becoming Heikou's memory problem.
	maxTranscriptBytes = 64 << 20
	// maxLineBytes bounds a single record. One tool result carrying a large
	// file is a normal thing to find in a transcript; it is not a reason to
	// hold it in memory. An over-long line is skipped and counted rather than
	// ending the scan, so a giant record in the middle does not hide the turns
	// after it.
	maxLineBytes = 1 << 20
	// maxTurnRunes bounds what one turn contributes. A turn is a summary of
	// what was said, and something has to stop a single pasted file from being
	// the whole answer.
	maxTurnRunes = 4000
	// maxTurnTools bounds the distinct tool names recorded for one turn. Calls
	// are counted rather than listed, so this caps the vocabulary of a turn and
	// not its length; a real turn has well under a dozen.
	maxTurnTools = 32
	// maxProjectDirectories bounds the fallback scan for a session's file.
	maxProjectDirectories = 4096

	// maxRolloutCandidates bounds how many Codex rollout files one match reads.
	// The scan is already narrowed to three day-directories; this stops a
	// pathological day from turning a lookup into a full-disk read.
	maxRolloutCandidates = 2048
	// maxRolloutHeadRecords bounds how far into a rollout the scan reads looking
	// for the first real user message. Codex writes a handful of injected
	// developer and environment records first; a file that has not reached the
	// prompt by here is not going to.
	maxRolloutHeadRecords = 40

	// CodexMatchWindow is how long after a launch Codex may write its rollout
	// and still be recognised as that launch. It is generous because it is not
	// what makes a match trustworthy — the launch directory and the verbatim
	// initial prompt are — and a cold start on a loaded machine is slow.
	CodexMatchWindow = 10 * time.Minute
	// codexMatchBackdate tolerates a rollout timestamped fractionally before the
	// durable record. Codex cannot start before Heikou asked it to, so this
	// covers rounding rather than a real ordering.
	codexMatchBackdate = 5 * time.Second
)

// ErrConversationNotFound means no runner-written record matched the launch. It
// is the normal answer for a session that never started, whose rollout has been
// deleted, or that Codex has not written yet.
var ErrConversationNotFound = errors.New("no runner conversation matches this launch")

// ErrConversationAmbiguous means more than one record matched. It is deliberately
// an error and not a choice: picking the nearest would attribute one session's
// conversation to another, and a wrong id resumes the wrong work.
var ErrConversationAmbiguous = errors.New("more than one runner conversation matches this launch")

// Availability separates the three answers a caller must be able to tell apart:
// a transcript that was read, a runner that keeps one but has none for this
// session, and a runner Heikou cannot locate a transcript for at all. Collapsing
// the last two would report a design limit as a missing file.
type Availability string

const (
	// Available means turns were read from a runner-written file.
	Available Availability = "available"
	// Missing means the runner writes transcripts and none was found for this
	// session. A session that never started, or whose transcript was deleted,
	// lands here.
	Missing Availability = "missing"
	// Unsupported means Heikou cannot locate a transcript for this runner, for
	// a structural reason rather than a missing file.
	Unsupported Availability = "unsupported"
)

// Role is who produced a turn. Tool results are not a role: they are what a
// tool returned to the assistant, and reporting them as user turns would make
// a session look like a conversation nobody had.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// ToolUse is one tool a turn ran, and how many times. Counting beats listing:
// a working turn calls Bash thirty times, and "Bash ×30" is the fact a reader
// wants while thirty repetitions of the word is not.
type ToolUse struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Turn is one contiguous stretch of a conversation by a single author. An
// assistant turn spans every record the runner wrote between two user
// messages, because that is one reply from the reader's point of view even
// when the runner stored it as thirty appends.
type Turn struct {
	// Index is the turn's position in the whole transcript, one-based, and
	// keeps its meaning after Last trims the head. A caller reading turns
	// 81-100 of 100 can tell that is where it is.
	Index int       `json:"index"`
	Role  Role      `json:"role"`
	At    time.Time `json:"at"`
	Text  string    `json:"text"`
	// Tools is what the turn ran, ordered by first call.
	Tools []ToolUse `json:"tools,omitempty"`
	// Truncated marks a turn this package did not report in full, because its
	// text hit maxTurnRunes or it ran more distinct tools than maxTurnTools.
	Truncated bool `json:"truncated,omitempty"`
}

// Transcript is the projection of one session's recorded history. It always
// names the runner that supplied it, so a caller can tell an authoritative
// record from an absent one rather than inferring it from an empty list.
type Transcript struct {
	SessionID    string         `json:"session_id"`
	Runner       heikou.Backend `json:"runner"`
	Availability Availability   `json:"availability"`
	// Reason explains anything other than Available, in one line.
	Reason string `json:"reason,omitempty"`
	// Path is where the turns were read from. It is reported so a caller can
	// verify the answer against the file, and is empty unless Available.
	Path string `json:"path,omitempty"`
	// TotalTurns counts the whole transcript, before Last trimmed it.
	TotalTurns int    `json:"total_turns"`
	Turns      []Turn `json:"turns"`
	// SkippedRecords counts records that could not be read: malformed JSON, or
	// a line past maxLineBytes. It is reported rather than swallowed, because a
	// silent skip reads exactly like having covered everything.
	SkippedRecords int `json:"skipped_records,omitempty"`
	// Bounded is true when the file was longer than maxTranscriptBytes and the
	// tail was never read. The turns are then the beginning of the transcript,
	// not the end, which is the opposite of what Last implies — so a caller
	// must be told.
	Bounded bool `json:"bounded,omitempty"`
}

// Request names one session to read. Root is the directory the session was
// launched in, which is how Claude Code files its transcripts.
type Request struct {
	Runner    heikou.Backend
	SessionID string
	Root      string
	// Last is how many recent turns to keep. Zero means DefaultTurns; a
	// negative value means every turn.
	Last int
}

// Reader locates and parses runner-written transcripts.
//
// ClaudeProjects and CodexSessions are fields rather than constants so tests
// can point at a fixture directory. Empty means the real location under the
// user's home.
type Reader struct {
	ClaudeProjects string
	CodexSessions  string
}

// ConversationRequest describes one launch precisely enough to recognise the
// record a runner wrote for it. Every field is durable Heikou state, so the
// question can be asked long after the pane is gone and gets the same answer.
type ConversationRequest struct {
	Runner heikou.Backend
	// Root is the directory the session was launched in.
	Root string
	// StartedAt is when Heikou created the durable record, which is immediately
	// before it asked the runtime to start.
	StartedAt time.Time
	// Prompt is the launch prompt, verbatim. It is the discriminator that makes
	// a match evidence rather than a guess: two sessions in one directory
	// minutes apart are common in Heikou and are told apart by what was asked.
	Prompt string
}

// Conversation is a runner-minted conversation Heikou matched to a launch. Path
// is reported so the match can be checked against the file it came from.
type Conversation struct {
	ID   string
	Path string
}

// FindConversation identifies the conversation a runner minted for one launch,
// for runners that do not let Heikou name it.
//
// It exists only for Codex. Claude takes --session-id, so Heikou never has to
// look: the id is whatever Heikou chose, and asking the filesystem would turn a
// certainty into an inference. Calling this for any other runner is a
// programming error and says so.
//
// A match requires three things to agree: the launch directory, a rollout
// written inside the match window, and the verbatim initial prompt. Anything
// less than exactly one match is an error — ErrConversationNotFound or
// ErrConversationAmbiguous — because the caller is about to write the answer
// into durable state and resume against it.
func (r Reader) FindConversation(request ConversationRequest) (Conversation, error) {
	if request.Runner != heikou.BackendCodex {
		return Conversation{}, fmt.Errorf(
			"find conversation: runner %q does not mint its own conversation id", request.Runner)
	}
	root := strings.TrimSpace(request.Root)
	if root == "" {
		return Conversation{}, errors.New("find conversation: root is empty")
	}
	if request.StartedAt.IsZero() {
		return Conversation{}, errors.New("find conversation: started_at is zero")
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return Conversation{}, errors.New("find conversation: prompt is empty")
	}
	sessions := r.codexSessions()
	if sessions == "" {
		return Conversation{}, ErrConversationNotFound
	}

	root = filepath.Clean(root)
	earliest := request.StartedAt.Add(-codexMatchBackdate)
	latest := request.StartedAt.Add(CodexMatchWindow)

	var matches []Conversation
	read := 0
	for _, day := range codexDayDirectories(sessions, request.StartedAt) {
		entries, err := os.ReadDir(day)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return Conversation{}, fmt.Errorf("read %s: %w", format.CompactPath(day), err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "rollout-") ||
				!strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			if read++; read > maxRolloutCandidates {
				// Reporting ambiguity beats reporting absence: the scan stopped
				// early, so "not found" would be a claim the scan never earned.
				return Conversation{}, ErrConversationAmbiguous
			}
			path := filepath.Join(day, entry.Name())
			if !isRegularFile(path) {
				continue
			}
			id, ok := matchCodexRollout(path, root, request.Prompt, earliest, latest)
			if !ok {
				continue
			}
			matches = append(matches, Conversation{ID: id, Path: path})
			if len(matches) > 1 {
				return Conversation{}, ErrConversationAmbiguous
			}
		}
	}
	if len(matches) == 0 {
		return Conversation{}, ErrConversationNotFound
	}
	return matches[0], nil
}

func (r Reader) codexSessions() string {
	if strings.TrimSpace(r.CodexSessions) != "" {
		return r.CodexSessions
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

// codexDayDirectories names the day directories a launch could have landed in.
// Codex partitions by local date while stamping records in UTC, so the day
// either side of the launch is included rather than reasoning about which
// convention applies at a given offset.
func codexDayDirectories(sessions string, startedAt time.Time) []string {
	days := make([]string, 0, 3)
	for _, offset := range []int{-1, 0, 1} {
		day := startedAt.AddDate(0, 0, offset)
		days = append(days, filepath.Join(sessions,
			fmt.Sprintf("%04d", day.Year()),
			fmt.Sprintf("%02d", int(day.Month())),
			fmt.Sprintf("%02d", day.Day())))
	}
	return days
}

// codexSessionMeta is the first record of a rollout, which is where Codex
// states the id it minted and the directory it started in.
type codexSessionMeta struct {
	Type    string `json:"type"`
	Payload struct {
		ID        string    `json:"id"`
		CWD       string    `json:"cwd"`
		Timestamp time.Time `json:"timestamp"`
	} `json:"payload"`
}

// codexRolloutRecord is the subset of a later rollout line the match reads. Only
// the first genuine user message is wanted, so nothing else is decoded.
type codexRolloutRecord struct {
	Type    string `json:"type"`
	Payload struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"payload"`
}

// matchCodexRollout reports whether one rollout is the record of this launch.
//
// It reads the head of the file only. The first record names the directory and
// the time; the prompt appears a few records later, after the developer and
// environment messages Codex injects.
func matchCodexRollout(path, root, prompt string, earliest, latest time.Time) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()

	limited := &io.LimitedReader{R: file, N: maxTranscriptBytes}
	reader := bufio.NewReaderSize(limited, 64<<10)

	line, _, err := readBoundedLine(reader, maxLineBytes)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false
	}
	var meta codexSessionMeta
	if json.Unmarshal(line, &meta) != nil || meta.Type != "session_meta" {
		return "", false
	}
	if meta.Payload.ID == "" || filepath.Clean(meta.Payload.CWD) != root {
		return "", false
	}
	at := meta.Payload.Timestamp
	if at.IsZero() || at.Before(earliest) || at.After(latest) {
		return "", false
	}
	if errors.Is(err, io.EOF) {
		return "", false
	}

	for records := 0; records < maxRolloutHeadRecords; records++ {
		line, _, readErr := readBoundedLine(reader, maxLineBytes)
		if text, ok := codexUserPrompt(line); ok {
			return meta.Payload.ID, text == prompt
		}
		if readErr != nil {
			return "", false
		}
	}
	return "", false
}

// codexUserPrompt returns the text of a genuine user message, and reports false
// for everything else — including the environment context Codex injects under
// the user role before the person's first word. Trusting the role would compare
// the launch prompt against a machine-written preamble and never match.
func codexUserPrompt(line []byte) (string, bool) {
	if len(bytes.TrimSpace(line)) == 0 {
		return "", false
	}
	var record codexRolloutRecord
	if json.Unmarshal(line, &record) != nil {
		return "", false
	}
	if record.Type != "response_item" || record.Payload.Type != "message" ||
		record.Payload.Role != "user" {
		return "", false
	}
	var builder strings.Builder
	for _, block := range record.Payload.Content {
		if block.Type == "input_text" || block.Type == "text" {
			builder.WriteString(block.Text)
		}
	}
	text := builder.String()
	if text == "" || strings.HasPrefix(strings.TrimSpace(text), "<environment_context>") {
		return "", false
	}
	return text, true
}

// Read returns what the runner recorded, or a plain statement that it recorded
// nothing findable. An error is reserved for a filesystem that misbehaved; a
// transcript that is simply not there is a successful answer with Availability
// set to Missing.
func (r Reader) Read(request Request) (Transcript, error) {
	result := Transcript{
		SessionID: request.SessionID,
		Runner:    request.Runner,
		Turns:     []Turn{},
	}
	if strings.TrimSpace(request.SessionID) == "" {
		return result, errors.New("read transcript: session id is empty")
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

	turns, stats, err := readClaudeTurns(path)
	if err != nil {
		return result, err
	}
	result.Availability = Available
	result.Path = path
	result.TotalTurns = len(turns)
	result.SkippedRecords = stats.skipped
	result.Bounded = stats.bounded
	result.Turns = lastTurns(turns, request.Last)
	return result, nil
}

// unsupportedReason says why a runner has no locatable transcript, in terms of
// what would have to change rather than as a bare refusal.
func unsupportedReason(runner heikou.Backend) string {
	switch runner {
	case heikou.BackendCodex:
		// Codex does write a rollout file per session, but it mints its own
		// session id and Heikou never learns it. Matching one by launch
		// directory and start time would be a guess, and a guess that silently
		// attributes one session's history to another is worse than no answer.
		return "codex mints its own session id, so Heikou cannot identify this session's rollout file"
	case heikou.BackendNoAgent:
		return "a no-agent session is a plain shell and records no transcript"
	default:
		return fmt.Sprintf("runner %q records no transcript Heikou can locate", runner)
	}
}

func lastTurns(turns []Turn, last int) []Turn {
	if last < 0 {
		return turns
	}
	if last == 0 {
		last = DefaultTurns
	}
	if len(turns) <= last {
		return turns
	}
	return turns[len(turns)-last:]
}

func (r Reader) claudeProjects() string {
	if strings.TrimSpace(r.ClaudeProjects) != "" {
		return r.ClaudeProjects
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// locateClaude finds the JSONL file Claude Code wrote for a session, or returns
// an empty path when there is none.
//
// Claude files a transcript as <projects>/<slugged cwd>/<session id>.jsonl, and
// Heikou owns the session id — it launches `claude --session-id <id>` — so the
// file name is exact. The directory name is not: it is the launch directory
// with separators replaced, and the exact replacement rules belong to Claude.
// So the slug is a fast path, and the session id is the authority. When the
// slug misses, the fallback scan asks the only question that is actually
// reliable: which project directory holds a file named for this session.
func (r Reader) locateClaude(sessionID, root string) (string, error) {
	projects := r.claudeProjects()
	if projects == "" {
		return "", nil
	}
	name := sessionID + ".jsonl"

	if slug := claudeProjectSlug(root); slug != "" {
		candidate := filepath.Join(projects, slug, name)
		if isRegularFile(candidate) {
			return candidate, nil
		}
	}

	entries, err := os.ReadDir(projects)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Claude Code is not installed, or has never run. That is a normal
			// state of the world, not a failure to report.
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", format.CompactPath(projects), err)
	}
	if len(entries) > maxProjectDirectories {
		entries = entries[:maxProjectDirectories]
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(projects, entry.Name(), name)
		if isRegularFile(candidate) {
			return candidate, nil
		}
	}
	return "", nil
}

// claudeProjectSlug reproduces Claude Code's directory naming for a launch
// directory. It is only ever used as a first guess; locateClaude falls back to
// a scan precisely because this rule is not Heikou's to define.
func claudeProjectSlug(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	return strings.NewReplacer("/", "-", ".", "-", "_", "-", " ", "-").Replace(filepath.Clean(root))
}

// isRegularFile keeps the reader off anything that is not a file. A path under
// the runner's home should never be a fifo or a device, and opening one anyway
// would block a CLI verb forever.
func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// claudeRecord is the subset of a transcript line this package reads. Claude
// writes many more record types — titles, attachments, hook results, queue
// operations — and the JSON decoder ignores every field not named here, so a
// new one appearing is not a parse failure.
type claudeRecord struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	// IsSidechain marks a subagent's conversation. It is excluded: a subagent's
	// turns are not turns of this session, and interleaving them would report a
	// conversation that never happened in that order.
	IsSidechain bool `json:"isSidechain"`
	// IsMeta marks a record the harness injected rather than a person or the
	// model authoring it.
	IsMeta bool `json:"isMeta"`
	// IsCompactSummary marks the summary written when a session runs out of
	// context and continues. It is stored under the user role and is often the
	// longest record in the file, but nobody said it: it restates turns that
	// are already in the transcript, so counting it as a turn would report the
	// same work twice and bury the real messages around it.
	IsCompactSummary bool `json:"isCompactSummary"`
	Message          *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// contentBlock is one element of a structured message body.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
}

type readStats struct {
	skipped int
	bounded bool
}

func readClaudeTurns(path string) ([]Turn, readStats, error) {
	var stats readStats
	file, err := os.Open(path)
	if err != nil {
		return nil, stats, fmt.Errorf("open transcript %s: %w", format.CompactPath(path), err)
	}
	defer file.Close()

	limited := &io.LimitedReader{R: file, N: maxTranscriptBytes}
	reader := bufio.NewReaderSize(limited, 64<<10)

	var (
		turns   []Turn
		pending *turnBuilder
	)
	flush := func() {
		if pending == nil {
			return
		}
		if turn, ok := pending.build(len(turns) + 1); ok {
			turns = append(turns, turn)
		}
		pending = nil
	}

	for {
		line, skipped, readErr := readBoundedLine(reader, maxLineBytes)
		if skipped {
			stats.skipped++
		}
		// A blank line is not a malformed record: a file being appended to ends
		// in one routinely, and counting it would report damage that is not there.
		if len(bytes.TrimSpace(line)) > 0 {
			record, ok := decodeRecord(line)
			if !ok {
				stats.skipped++
			} else if role, texts, tools, skip := interpret(record); !skip {
				switch role {
				case RoleUser:
					flush()
					pending = &turnBuilder{role: RoleUser, at: record.Timestamp}
					pending.add(texts, tools)
					flush()
				case RoleAssistant:
					if pending == nil {
						pending = &turnBuilder{role: RoleAssistant, at: record.Timestamp}
					}
					pending.add(texts, tools)
				}
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return nil, stats, fmt.Errorf("read transcript %s: %w", format.CompactPath(path), readErr)
			}
			break
		}
	}
	flush()

	// N reaching zero means the limit stopped the read rather than the file
	// ending, so the tail was never seen.
	stats.bounded = limited.N == 0
	return turns, stats, nil
}

func decodeRecord(line []byte) (claudeRecord, bool) {
	var record claudeRecord
	if err := json.Unmarshal(line, &record); err != nil {
		return claudeRecord{}, false
	}
	return record, true
}

// interpret decides what one record contributes, and reports skip for a record
// that contributes nothing at all.
//
// The load-bearing case is the last one. Claude stores a tool's return value as
// a record under the user role, so a parser that trusts the role reports every
// tool result as something the user said. That is the single most misleading
// thing this code could do, and it is the default behaviour if nobody looks.
func interpret(record claudeRecord) (role Role, texts []string, tools []string, skip bool) {
	if record.IsSidechain || record.IsMeta || record.IsCompactSummary || record.Message == nil {
		return "", nil, nil, true
	}
	switch record.Type {
	case "user":
		role = RoleUser
	case "assistant":
		role = RoleAssistant
	default:
		return "", nil, nil, true
	}
	texts, tools, onlyToolResults := decodeContent(record.Message.Content)
	if onlyToolResults {
		return role, nil, nil, true
	}
	if role == RoleUser && len(texts) == 1 {
		if command, drop, matched := localCommandEnvelope(texts[0]); matched {
			if drop {
				return role, nil, nil, true
			}
			texts = []string{command}
		}
	}
	return role, texts, tools, false
}

// Local slash commands reach the transcript inside a machine-written wrapper,
// as a record under the user role whose whole content is the envelope. Left
// alone they read as things the user typed, so `/compact` appears as a message
// saying "<command-name>/compact</command-name>" followed by another saying
// "<local-command-stdout>Compacted </local-command-stdout>".
const (
	commandNameOpen  = "<command-name>"
	commandNameClose = "</command-name>"
	localStdoutOpen  = "<local-command-stdout>"
	localCaveatOpen  = "<local-command-caveat>"
)

// localCommandEnvelope recognises that wrapper and reduces it to what happened:
// the command the user ran, or nothing when the record is only its output.
//
// This matches on exact tags that Claude Code writes, never on the shape of
// prose, so the one way it can be wrong is a user whose message begins with a
// literal "<command-name>". That message would be reported as a command run
// rather than as text. The alternative — printing the raw envelope — is wrong
// on every session that ever used a slash command.
func localCommandEnvelope(text string) (command string, drop bool, matched bool) {
	trimmed := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(trimmed, commandNameOpen),
		strings.HasPrefix(trimmed, localStdoutOpen),
		strings.HasPrefix(trimmed, localCaveatOpen):
	default:
		return "", false, false
	}
	start := strings.Index(trimmed, commandNameOpen)
	if start < 0 {
		// Command output, with no command named. It is the local equivalent of
		// a tool result: something a turn produced, not a turn.
		return "", true, true
	}
	rest := trimmed[start+len(commandNameOpen):]
	end := strings.Index(rest, commandNameClose)
	if end < 0 {
		return "", true, true
	}
	if command = strings.TrimSpace(rest[:end]); command == "" {
		return "", true, true
	}
	return command, false, true
}

// decodeContent reads a message body, which Claude writes either as a plain
// string or as an array of typed blocks.
func decodeContent(raw json.RawMessage) (texts []string, tools []string, onlyToolResults bool) {
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
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil || len(blocks) == 0 {
		return nil, nil, false
	}
	onlyToolResults = true
	for _, block := range blocks {
		switch block.Type {
		case "tool_result":
			continue
		case "text":
			if trimmed := strings.TrimSpace(block.Text); trimmed != "" {
				texts = append(texts, trimmed)
			}
		case "tool_use":
			if block.Name != "" {
				tools = append(tools, block.Name)
			}
		default:
			// Thinking blocks land here and are dropped deliberately. A turn
			// reports what was said and what was run; the model's private
			// reasoning is neither, and it is the bulk of the bytes.
		}
		onlyToolResults = false
	}
	return texts, tools, onlyToolResults
}

// turnBuilder accumulates the records that make up one turn.
type turnBuilder struct {
	role      Role
	at        time.Time
	texts     []string
	tools     []ToolUse
	toolIndex map[string]int
	truncated bool
	runes     int
}

func (b *turnBuilder) add(texts, tools []string) {
	for _, text := range texts {
		if b.runes >= maxTurnRunes {
			b.truncated = true
			break
		}
		clean := format.Sanitize(text)
		if count := utf8.RuneCountInString(clean); b.runes+count > maxTurnRunes {
			clean = truncateRunes(clean, maxTurnRunes-b.runes)
			b.truncated = true
		}
		b.runes += utf8.RuneCountInString(clean)
		if clean != "" {
			b.texts = append(b.texts, clean)
		}
	}
	for _, tool := range tools {
		if position, seen := b.toolIndex[tool]; seen {
			b.tools[position].Count++
			continue
		}
		if len(b.tools) >= maxTurnTools {
			b.truncated = true
			continue
		}
		if b.toolIndex == nil {
			b.toolIndex = make(map[string]int, 8)
		}
		b.toolIndex[tool] = len(b.tools)
		b.tools = append(b.tools, ToolUse{Name: tool, Count: 1})
	}
}

// build returns the finished turn, or false when the records contributed
// nothing a reader would want. An assistant stretch that only thought, and a
// user record that only carried an attachment, are both real records and empty
// turns.
func (b *turnBuilder) build(index int) (Turn, bool) {
	if len(b.texts) == 0 && len(b.tools) == 0 {
		return Turn{}, false
	}
	return Turn{
		Index:     index,
		Role:      b.role,
		At:        b.at,
		Text:      strings.Join(b.texts, "\n\n"),
		Tools:     b.tools,
		Truncated: b.truncated,
	}, true
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	count := 0
	for index := range value {
		if count == limit {
			return value[:index]
		}
		count++
	}
	return value
}

// readBoundedLine reads one newline-delimited line, discarding any line longer
// than limit and reporting that it did. Returning the skip rather than an error
// is what lets one oversized record be stepped over instead of ending the scan.
func readBoundedLine(reader *bufio.Reader, limit int) (line []byte, skipped bool, err error) {
	for {
		chunk, chunkErr := reader.ReadSlice('\n')
		if !skipped {
			if len(line)+len(chunk) > limit {
				skipped, line = true, nil
			} else {
				// ReadSlice returns a view into the reader's buffer, which the
				// next read invalidates, so this copy is load-bearing.
				line = append(line, chunk...)
			}
		}
		if chunkErr == nil {
			return line, skipped, nil
		}
		if !errors.Is(chunkErr, bufio.ErrBufferFull) {
			return line, skipped, chunkErr
		}
	}
}
