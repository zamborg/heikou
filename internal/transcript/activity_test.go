package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zamborg/heikou/internal/heikou"
)

func toolUseRecord(t *testing.T, stopReason string, blocks ...map[string]any) string {
	t.Helper()
	return record(t, map[string]any{
		"type":      "assistant",
		"timestamp": "2026-08-13T10:00:01Z",
		"message":   map[string]any{"role": "assistant", "content": blocks, "stop_reason": stopReason},
	})
}

func toolCall(id, name string, input map[string]any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

func toolResultRecord(t *testing.T, id string) string {
	t.Helper()
	return record(t, map[string]any{
		"type":      "user",
		"timestamp": "2026-08-13T10:00:02Z",
		"message": map[string]any{"role": "user", "content": []map[string]any{
			{"type": "tool_result", "tool_use_id": id, "content": "done"},
		}},
	})
}

func readActivity(t *testing.T, projects string) Activity {
	t.Helper()
	item, err := Reader{ClaudeProjects: projects}.ReadActivity(Request{
		Runner: heikou.BackendClaude, SessionID: testSessionID, Root: "/tmp/project",
	})
	if err != nil {
		t.Fatalf("ReadActivity: %v", err)
	}
	return item
}

func TestActivityReportsTheToolACallNamed(t *testing.T) {
	tests := map[string]struct {
		block       map[string]any
		wantTool    string
		wantDetail  string
		wantSubject string
	}{
		"a shell call reports its command, not the prose beside it": {
			block: toolCall("t1", "Bash", map[string]any{
				"command": "make check", "description": "Run the full check suite",
			}),
			wantTool: "Bash", wantDetail: "make check", wantSubject: "make check",
		},
		"an edit reports the file, and a row gets its base name": {
			block:    toolCall("t1", "Edit", map[string]any{"file_path": "/repo/internal/brief/observer.go"}),
			wantTool: "Edit", wantDetail: "/repo/internal/brief/observer.go", wantSubject: "observer.go",
		},
		"a search reports its pattern": {
			block:    toolCall("t1", "Grep", map[string]any{"pattern": "BriefSource"}),
			wantTool: "Grep", wantDetail: "BriefSource", wantSubject: "BriefSource",
		},
		"an unknown tool contributes its name and no wrong subject": {
			block:    toolCall("t1", "MysteryTool", map[string]any{"whatever": "value"}),
			wantTool: "MysteryTool", wantDetail: "", wantSubject: "",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			projects := writeTranscript(t, "-tmp-project",
				userRecord(t, "do the thing"),
				toolUseRecord(t, "tool_use", test.block))
			item := readActivity(t, projects)
			if item.Kind != ActivityRunning || item.Tool != test.wantTool {
				t.Fatalf("activity = %+v", item)
			}
			if item.Detail != test.wantDetail {
				t.Fatalf("detail = %q, want %q", item.Detail, test.wantDetail)
			}
			if item.Subject() != test.wantSubject {
				t.Fatalf("subject = %q, want %q", item.Subject(), test.wantSubject)
			}
		})
	}
}

// A tool result always follows the call it answers, so the last record in the
// file is the one that says what is happening. Reading the call alone would
// report every finished session as still running its last command.
func TestAFinishedToolIsNoLongerRunning(t *testing.T) {
	projects := writeTranscript(t, "-tmp-project",
		userRecord(t, "do the thing"),
		toolUseRecord(t, "tool_use", toolCall("t1", "Bash", map[string]any{"command": "make check"})),
		toolResultRecord(t, "t1"))
	if item := readActivity(t, projects); item.Kind != ActivityWorking || item.Tool != "" {
		t.Fatalf("activity = %+v, want working with no tool", item)
	}
}

func TestAnEndedTurnReportsWhatTheReplySaid(t *testing.T) {
	projects := writeTranscript(t, "-tmp-project",
		userRecord(t, "do the thing"),
		toolUseRecord(t, "tool_use", toolCall("t1", "Bash", map[string]any{"command": "make check"})),
		toolResultRecord(t, "t1"),
		toolUseRecord(t, "end_turn", map[string]any{"type": "text", "text": "make check is green"}))
	item := readActivity(t, projects)
	if item.Kind != ActivityReplied {
		t.Fatalf("activity = %+v, want replied", item)
	}
	if item.Detail != "make check is green" {
		t.Fatalf("detail = %q", item.Detail)
	}
}

// An assistant record with no stop reason is a record mid-flight, not a turn
// that ended. Reading it as an ending would report a working session as
// finished on every intermediate append.
func TestAnAbsentStopReasonIsNotAnEnding(t *testing.T) {
	projects := writeTranscript(t, "-tmp-project",
		userRecord(t, "do the thing"),
		toolUseRecord(t, "", map[string]any{"type": "text", "text": "let me look"}))
	if item := readActivity(t, projects); item.Kind != ActivityWorking {
		t.Fatalf("activity = %+v, want working", item)
	}
}

func TestSubagentActivityIsNotThisSessionsActivity(t *testing.T) {
	sidechain := record(t, map[string]any{
		"type": "assistant", "timestamp": "2026-08-13T10:00:09Z", "isSidechain": true,
		"message": map[string]any{"role": "assistant", "stop_reason": "tool_use", "content": []map[string]any{
			toolCall("t9", "Read", map[string]any{"file_path": "/repo/subagent.go"}),
		}},
	})
	projects := writeTranscript(t, "-tmp-project",
		userRecord(t, "do the thing"),
		toolUseRecord(t, "tool_use", toolCall("t1", "Bash", map[string]any{"command": "make check"})),
		sidechain)
	item := readActivity(t, projects)
	if item.Tool != "Bash" {
		t.Fatalf("a subagent's tool was reported as this session's: %+v", item)
	}
}

// The runner appends while this reads, so the file routinely ends in half a
// record. That is normal operation and must not lose the record before it.
func TestAHalfWrittenLastRecordIsIgnoredRatherThanFatal(t *testing.T) {
	projects := writeTranscript(t, "-tmp-project",
		userRecord(t, "do the thing"),
		toolUseRecord(t, "tool_use", toolCall("t1", "Bash", map[string]any{"command": "make check"})),
		`{"type":"assistant","timestamp":"2026-08-13T10`)
	if item := readActivity(t, projects); item.Kind != ActivityRunning || item.Detail != "make check" {
		t.Fatalf("activity = %+v", item)
	}
}

// A single tool result carrying a large file can be longer than the window. The
// reader then knows nothing, and saying so is what lets a caller fall through
// instead of showing a stale or invented line.
func TestARecordLargerThanTheWindowLeavesNothingKnown(t *testing.T) {
	giant := record(t, map[string]any{
		"type": "user", "timestamp": "2026-08-13T10:00:02Z",
		"message": map[string]any{"role": "user", "content": []map[string]any{
			{"type": "tool_result", "tool_use_id": "t1", "content": strings.Repeat("x", activityTailBytes+1024)},
		}},
	})
	projects := writeTranscript(t, "-tmp-project",
		toolUseRecord(t, "tool_use", toolCall("t1", "Bash", map[string]any{"command": "make check"})),
		giant)
	item := readActivity(t, projects)
	if item.Availability != Available {
		t.Fatalf("availability = %q, want available; the file was found", item.Availability)
	}
	if item.Known() || item.Kind != ActivityUnknown {
		t.Fatalf("activity = %+v, want nothing known", item)
	}
}

// The window is a tail, so a long transcript must still report its end rather
// than whatever the seek happened to land on.
func TestOnlyTheEndOfALongTranscriptIsRead(t *testing.T) {
	lines := []string{userRecord(t, "do the thing")}
	filler := record(t, map[string]any{
		"type": "user", "timestamp": "2026-08-13T10:00:02Z",
		"message": map[string]any{"role": "user", "content": []map[string]any{
			{"type": "tool_result", "tool_use_id": "old", "content": strings.Repeat("y", 4096)},
		}},
	})
	for range 64 {
		lines = append(lines, filler)
	}
	lines = append(lines, toolUseRecord(t, "tool_use",
		toolCall("t1", "Edit", map[string]any{"file_path": "/repo/late.go"})))
	projects := writeTranscript(t, "-tmp-project", lines...)

	info, err := os.Stat(filepath.Join(projects, "-tmp-project", testSessionID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= activityTailBytes {
		t.Fatalf("fixture is %d bytes; it has to exceed the %d-byte window to prove anything", info.Size(), activityTailBytes)
	}
	if item := readActivity(t, projects); item.Subject() != "late.go" {
		t.Fatalf("activity = %+v, want the last record", item)
	}
}

func TestActivityForAMissingTranscriptIsAnAnswerNotAnError(t *testing.T) {
	item, err := Reader{ClaudeProjects: t.TempDir()}.ReadActivity(Request{
		Runner: heikou.BackendClaude, SessionID: testSessionID, Root: "/tmp/project",
	})
	if err != nil {
		t.Fatalf("ReadActivity: %v", err)
	}
	if item.Availability != Missing || item.Known() {
		t.Fatalf("activity = %+v", item)
	}
}

func TestActivityForCodexReportsUnsupportedWithItsReason(t *testing.T) {
	item, err := Reader{ClaudeProjects: t.TempDir()}.ReadActivity(Request{
		Runner: heikou.BackendCodex, SessionID: testSessionID, Root: "/tmp/project",
	})
	if err != nil {
		t.Fatalf("ReadActivity: %v", err)
	}
	if item.Availability != Unsupported || item.Known() {
		t.Fatalf("activity = %+v", item)
	}
	if !strings.Contains(item.Reason, "session id") {
		t.Fatalf("reason does not say what would have to change: %q", item.Reason)
	}
}

func TestActivityRejectsAnEmptySessionID(t *testing.T) {
	if _, err := (Reader{}).ReadActivity(Request{Runner: heikou.BackendClaude}); err == nil {
		t.Fatal("ReadActivity() error = nil for an empty session id")
	}
}

// A command is written by the model and lands in a terminal, so an escape
// sequence in one must not survive to the row that draws it.
func TestControlSequencesAreStrippedFromAnActivity(t *testing.T) {
	projects := writeTranscript(t, "-tmp-project",
		toolUseRecord(t, "tool_use", toolCall("t1", "Bash", map[string]any{
			"command": "echo \x1b[31mred\x1b[0m\nsecond line",
		})))
	item := readActivity(t, projects)
	if strings.ContainsRune(item.Detail, 0x1b) || strings.ContainsRune(item.Detail, '\n') {
		t.Fatalf("detail kept terminal control: %q", item.Detail)
	}
	if item.Detail != "echo red second line" {
		t.Fatalf("detail = %q", item.Detail)
	}
}

func TestAnEnormousSubjectIsBounded(t *testing.T) {
	projects := writeTranscript(t, "-tmp-project",
		toolUseRecord(t, "tool_use", toolCall("t1", "Bash", map[string]any{
			"command": strings.Repeat("a", 10_000),
		})))
	if got := len([]rune(readActivity(t, projects).Detail)); got != activityDetailRunes {
		t.Fatalf("detail is %d runes, want the %d-rune bound", got, activityDetailRunes)
	}
}
