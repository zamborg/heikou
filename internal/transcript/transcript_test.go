package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zamborg/heikou/internal/heikou"
)

const testSessionID = "11111111-2222-4333-8444-555555555555"

// writeTranscript builds a Claude-shaped project directory containing one
// session file, and returns the projects root.
func writeTranscript(t *testing.T, directory string, lines ...string) string {
	t.Helper()
	projects := t.TempDir()
	full := filepath.Join(projects, directory)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatalf("create project directory: %v", err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(full, testSessionID+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return projects
}

func record(t *testing.T, value map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode record: %v", err)
	}
	return string(encoded)
}

func userRecord(t *testing.T, text string) string {
	t.Helper()
	return record(t, map[string]any{
		"type":      "user",
		"timestamp": "2026-08-13T10:00:00Z",
		"message":   map[string]any{"role": "user", "content": text},
	})
}

func assistantRecord(t *testing.T, blocks ...map[string]any) string {
	t.Helper()
	return record(t, map[string]any{
		"type":      "assistant",
		"timestamp": "2026-08-13T10:00:01Z",
		"message":   map[string]any{"role": "assistant", "content": blocks},
	})
}

func textBlock(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

func toolUseBlock(name string) map[string]any {
	return map[string]any{"type": "tool_use", "name": name, "id": "toolu_1", "input": map[string]any{}}
}

func read(t *testing.T, projects string, request Request) Transcript {
	t.Helper()
	if request.SessionID == "" {
		request.SessionID = testSessionID
	}
	if request.Runner == "" {
		request.Runner = heikou.BackendClaude
	}
	result, err := Reader{ClaudeProjects: projects}.Read(request)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	return result
}

// A tool returning to the assistant is stored under the user role. Reporting it
// as a user turn would invent a conversation, so this is the parser's most
// important behaviour.
func TestToolResultsAreNotUserTurns(t *testing.T) {
	projects := writeTranscript(t, "-work",
		userRecord(t, "add the retry"),
		assistantRecord(t, textBlock("looking now"), toolUseBlock("Read")),
		record(t, map[string]any{
			"type":      "user",
			"timestamp": "2026-08-13T10:00:02Z",
			"message": map[string]any{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "toolu_1", "content": "file contents"},
			}},
		}),
		assistantRecord(t, textBlock("done")),
	)

	result := read(t, projects, Request{Root: "/work", Last: -1})
	if result.Availability != Available {
		t.Fatalf("availability = %q, want %q (%s)", result.Availability, Available, result.Reason)
	}
	if result.TotalTurns != 2 {
		t.Fatalf("total turns = %d, want 2: %+v", result.TotalTurns, result.Turns)
	}
	if result.Turns[0].Role != RoleUser || result.Turns[0].Text != "add the retry" {
		t.Fatalf("first turn = %+v, want the user's message", result.Turns[0])
	}
	// Both assistant stretches collapse into one turn because no user message
	// separates them: a tool result is not a turn boundary.
	if result.Turns[1].Role != RoleAssistant {
		t.Fatalf("second turn role = %q, want assistant", result.Turns[1].Role)
	}
	if result.Turns[1].Text != "looking now\n\ndone" {
		t.Fatalf("assistant text = %q, want both replies joined", result.Turns[1].Text)
	}
	for _, turn := range result.Turns {
		if strings.Contains(turn.Text, "file contents") {
			t.Fatalf("tool output leaked into turn %d: %q", turn.Index, turn.Text)
		}
	}
}

// A user message is what ends an assistant turn, so a reply the runner stored
// as many appends reads as one thing that happened.
func TestUserMessagesAreTheTurnBoundary(t *testing.T) {
	projects := writeTranscript(t, "-work",
		userRecord(t, "first"),
		assistantRecord(t, textBlock("a")),
		assistantRecord(t, textBlock("b")),
		userRecord(t, "second"),
		assistantRecord(t, textBlock("c")),
	)

	result := read(t, projects, Request{Root: "/work", Last: -1})
	roles := make([]string, 0, len(result.Turns))
	for _, turn := range result.Turns {
		roles = append(roles, string(turn.Role))
	}
	if got := strings.Join(roles, ","); got != "user,assistant,user,assistant" {
		t.Fatalf("roles = %s, want alternating turns", got)
	}
	if result.Turns[1].Text != "a\n\nb" {
		t.Fatalf("first reply = %q, want both appends", result.Turns[1].Text)
	}
}

func TestThinkingIsDroppedAndToolCallsAreKept(t *testing.T) {
	projects := writeTranscript(t, "-work",
		userRecord(t, "ship it"),
		assistantRecord(t,
			map[string]any{"type": "thinking", "thinking": "the user probably wants"},
			toolUseBlock("Bash"),
		),
		assistantRecord(t, toolUseBlock("Edit"), textBlock("pushed")),
	)

	result := read(t, projects, Request{Root: "/work", Last: -1})
	reply := result.Turns[1]
	if strings.Contains(reply.Text, "probably wants") {
		t.Fatalf("thinking leaked into the turn: %q", reply.Text)
	}
	if reply.Text != "pushed" {
		t.Fatalf("reply text = %q, want only what was said", reply.Text)
	}
	if got := toolSummary(reply.Tools); got != "Bash×1,Edit×1" {
		t.Fatalf("tools = %s, want Bash then Edit in order", got)
	}
}

func toolSummary(tools []ToolUse) string {
	parts := make([]string, 0, len(tools))
	for _, tool := range tools {
		parts = append(parts, tool.Name+"×"+strconv.Itoa(tool.Count))
	}
	return strings.Join(parts, ",")
}

// A working turn calls the same tool over and over. Thirty repetitions of the
// word "Bash" is not what a reader is asking for.
func TestRepeatedToolCallsAreCountedInFirstCallOrder(t *testing.T) {
	blocks := []map[string]any{toolUseBlock("Bash"), toolUseBlock("Read")}
	for range 9 {
		blocks = append(blocks, toolUseBlock("Bash"))
	}
	projects := writeTranscript(t, "-work", userRecord(t, "go"), assistantRecord(t, blocks...))

	if got := toolSummary(read(t, projects, Request{Root: "/work", Last: -1}).Turns[1].Tools); got != "Bash×10,Read×1" {
		t.Fatalf("tools = %s, want counts ordered by first call", got)
	}
}

// A record that only thought, and one that only carried an attachment, are real
// records and empty turns. Emitting them would pad the history with blanks.
func TestRecordsThatSayNothingProduceNoTurn(t *testing.T) {
	projects := writeTranscript(t, "-work",
		assistantRecord(t, map[string]any{"type": "thinking", "thinking": "hmm"}),
		record(t, map[string]any{
			"type": "attachment", "timestamp": "2026-08-13T10:00:00Z",
			"attachment": map[string]any{"type": "file"},
		}),
		userRecord(t, "still here?"),
	)

	result := read(t, projects, Request{Root: "/work", Last: -1})
	if result.TotalTurns != 1 {
		t.Fatalf("total turns = %d, want only the user message: %+v", result.TotalTurns, result.Turns)
	}
	if result.Turns[0].Index != 1 {
		t.Fatalf("index = %d, want 1; empty records must not consume a number", result.Turns[0].Index)
	}
}

// A subagent's conversation is not this session's conversation, and
// interleaving it would report an order that never happened.
func TestSidechainAndMetaRecordsAreExcluded(t *testing.T) {
	sidechain := map[string]any{
		"type": "user", "timestamp": "2026-08-13T10:00:00Z", "isSidechain": true,
		"message": map[string]any{"role": "user", "content": "explore the repo"},
	}
	meta := map[string]any{
		"type": "user", "timestamp": "2026-08-13T10:00:00Z", "isMeta": true,
		"message": map[string]any{"role": "user", "content": "<injected context>"},
	}
	projects := writeTranscript(t, "-work",
		record(t, sidechain), record(t, meta), userRecord(t, "the real ask"),
	)

	result := read(t, projects, Request{Root: "/work", Last: -1})
	if result.TotalTurns != 1 || result.Turns[0].Text != "the real ask" {
		t.Fatalf("turns = %+v, want only the session's own message", result.Turns)
	}
}

// Last is a reading length. It trims the head, keeps the original numbering,
// and never changes what TotalTurns reports.
func TestLastTrimsTheHeadWithoutRenumbering(t *testing.T) {
	lines := make([]string, 0, 10)
	for index := range 10 {
		lines = append(lines, userRecord(t, string(rune('a'+index))))
	}
	projects := writeTranscript(t, "-work", lines...)

	result := read(t, projects, Request{Root: "/work", Last: 3})
	if result.TotalTurns != 10 {
		t.Fatalf("total turns = %d, want the whole transcript counted", result.TotalTurns)
	}
	if len(result.Turns) != 3 {
		t.Fatalf("returned %d turns, want 3", len(result.Turns))
	}
	if result.Turns[0].Index != 8 || result.Turns[2].Index != 10 {
		t.Fatalf("indexes = %d..%d, want 8..10", result.Turns[0].Index, result.Turns[2].Index)
	}
	if result.Turns[2].Text != "j" {
		t.Fatalf("last turn = %q, want the newest message", result.Turns[2].Text)
	}
}

func TestZeroLastUsesTheDefaultAndNegativeReturnsEverything(t *testing.T) {
	lines := make([]string, 0, DefaultTurns+5)
	for index := range DefaultTurns + 5 {
		lines = append(lines, userRecord(t, string(rune('a'+index%26))))
	}
	projects := writeTranscript(t, "-work", lines...)

	if got := len(read(t, projects, Request{Root: "/work"}).Turns); got != DefaultTurns {
		t.Fatalf("default returned %d turns, want %d", got, DefaultTurns)
	}
	if got := len(read(t, projects, Request{Root: "/work", Last: -1}).Turns); got != DefaultTurns+5 {
		t.Fatalf("negative last returned %d turns, want all %d", got, DefaultTurns+5)
	}
}

// The session id is the authority, not the directory name. Claude owns the
// slug rules, so a directory this code cannot predict must still be found.
func TestTranscriptIsFoundWhenTheDirectoryNameIsUnpredictable(t *testing.T) {
	projects := writeTranscript(t, "an-encoding-heikou-does-not-model", userRecord(t, "found me"))

	result := read(t, projects, Request{Root: "/somewhere/else", Last: -1})
	if result.Availability != Available {
		t.Fatalf("availability = %q, want the scan to find the file by session id", result.Availability)
	}
	if result.Turns[0].Text != "found me" {
		t.Fatalf("turns = %+v", result.Turns)
	}
}

func TestSlugLocatesTheTranscriptWithoutScanning(t *testing.T) {
	root := "/Users/z/Documents/ez-gz/heikou/.claude/worktrees/one"
	slug := claudeProjectSlug(root)
	if slug != "-Users-z-Documents-ez-gz-heikou--claude-worktrees-one" {
		t.Fatalf("slug = %q, want the separators and dots replaced", slug)
	}
	projects := writeTranscript(t, slug, userRecord(t, "fast path"))
	if got := read(t, projects, Request{Root: root, Last: -1}).Turns[0].Text; got != "fast path" {
		t.Fatalf("turn = %q", got)
	}
}

// A missing transcript is a normal answer. The file layout belongs to Claude,
// so its absence must never be an error a caller has to handle.
func TestMissingTranscriptIsAnAnswerNotAnError(t *testing.T) {
	result := read(t, t.TempDir(), Request{Root: "/work"})
	if result.Availability != Missing {
		t.Fatalf("availability = %q, want %q", result.Availability, Missing)
	}
	if result.Reason == "" {
		t.Fatal("a missing transcript must say where it looked")
	}
	if result.Turns == nil {
		t.Fatal("turns must be an empty list, not null, so --json stays one shape")
	}
}

func TestAbsentProjectsDirectoryIsMissingRatherThanAnError(t *testing.T) {
	result := read(t, filepath.Join(t.TempDir(), "never-installed"), Request{Root: "/work"})
	if result.Availability != Missing {
		t.Fatalf("availability = %q, want %q when Claude has never run", result.Availability, Missing)
	}
}

// Codex writes rollout files but mints its own session id, so Heikou cannot say
// which one belongs to this session. That is a different answer from "missing".
func TestCodexReportsUnsupportedRatherThanMissing(t *testing.T) {
	result := read(t, t.TempDir(), Request{Runner: heikou.BackendCodex, Root: "/work"})
	if result.Availability != Unsupported {
		t.Fatalf("availability = %q, want %q", result.Availability, Unsupported)
	}
	if !strings.Contains(result.Reason, "session id") {
		t.Fatalf("reason = %q, want it to name why the file cannot be identified", result.Reason)
	}
}

func TestNoAgentReportsUnsupported(t *testing.T) {
	result := read(t, t.TempDir(), Request{Runner: heikou.BackendNoAgent, Root: "/work"})
	if result.Availability != Unsupported {
		t.Fatalf("availability = %q, want %q", result.Availability, Unsupported)
	}
}

func TestEveryAnswerNamesItsRunner(t *testing.T) {
	for _, runner := range []heikou.Backend{heikou.BackendClaude, heikou.BackendCodex, heikou.BackendNoAgent} {
		result := read(t, t.TempDir(), Request{Runner: runner, Root: "/work"})
		if result.Runner != runner {
			t.Fatalf("runner = %q, want %q so a caller can tell what supplied the answer", result.Runner, runner)
		}
	}
}

// One oversized record must be stepped over, not treated as the end of the
// file, or a single pasted binary hides every turn after it.
func TestAnOversizedRecordIsSkippedAndTheScanContinues(t *testing.T) {
	huge := userRecord(t, strings.Repeat("x", maxLineBytes+1024))
	projects := writeTranscript(t, "-work",
		userRecord(t, "before"), huge, userRecord(t, "after"),
	)

	result := read(t, projects, Request{Root: "/work", Last: -1})
	if result.TotalTurns != 2 {
		t.Fatalf("total turns = %d, want the two readable messages: %+v", result.TotalTurns, result.Turns)
	}
	if result.Turns[1].Text != "after" {
		t.Fatalf("last turn = %q, want the record following the oversized one", result.Turns[1].Text)
	}
	if result.SkippedRecords != 1 {
		t.Fatalf("skipped = %d, want 1; a silent skip reads like full coverage", result.SkippedRecords)
	}
}

func TestMalformedRecordsAreCountedRatherThanFatal(t *testing.T) {
	projects := writeTranscript(t, "-work",
		userRecord(t, "before"), "{not json at all", "", userRecord(t, "after"),
	)

	result := read(t, projects, Request{Root: "/work", Last: -1})
	if result.TotalTurns != 2 {
		t.Fatalf("total turns = %d, want both readable messages", result.TotalTurns)
	}
	if result.SkippedRecords != 1 {
		t.Fatalf("skipped = %d, want 1; a blank line is not a malformed record", result.SkippedRecords)
	}
}

func TestAnEnormousTurnIsTruncatedAndSaysSo(t *testing.T) {
	projects := writeTranscript(t, "-work", userRecord(t, strings.Repeat("s", maxTurnRunes*2)))

	turn := read(t, projects, Request{Root: "/work", Last: -1}).Turns[0]
	if !turn.Truncated {
		t.Fatal("a truncated turn must say it was truncated")
	}
	if count := len([]rune(turn.Text)); count != maxTurnRunes {
		t.Fatalf("turn runes = %d, want %d", count, maxTurnRunes)
	}
}

// Terminal escape sequences reach a transcript through pasted output. Printing
// one would let a recorded session repaint the reader's terminal.
func TestControlSequencesAreStrippedFromTurns(t *testing.T) {
	projects := writeTranscript(t, "-work", userRecord(t, "clean\x1b[31mred\x1b[0m\x07 text"))

	turn := read(t, projects, Request{Root: "/work", Last: -1}).Turns[0]
	if strings.ContainsAny(turn.Text, "\x1b\x07") {
		t.Fatalf("turn kept a control sequence: %q", turn.Text)
	}
	if turn.Text != "cleanred text" {
		t.Fatalf("turn = %q, want the readable text preserved", turn.Text)
	}
}

// The summary written when a session runs out of context restates turns that
// are already in the file. Counting it would report the same work twice and
// bury the messages on either side of it.
func TestTheCompactSummaryIsNotATurn(t *testing.T) {
	summary := map[string]any{
		"type": "user", "timestamp": "2026-08-13T10:00:00Z",
		"isCompactSummary": true, "isVisibleInTranscriptOnly": true,
		"message": map[string]any{"role": "user", "content": "This session is being continued…"},
	}
	projects := writeTranscript(t, "-work",
		userRecord(t, "before the limit"), record(t, summary), userRecord(t, "after the limit"),
	)

	result := read(t, projects, Request{Root: "/work", Last: -1})
	if result.TotalTurns != 2 {
		t.Fatalf("total turns = %d, want the two real messages: %+v", result.TotalTurns, result.Turns)
	}
	for _, turn := range result.Turns {
		if strings.Contains(turn.Text, "being continued") {
			t.Fatalf("the compact summary was reported as turn %d", turn.Index)
		}
	}
}

// A slash command arrives wrapped in a machine-written envelope. Printed raw it
// reads as a message the user typed, which is what every session that has ever
// run /compact would show.
func TestLocalCommandsBecomeTheCommandAndTheirOutputIsDropped(t *testing.T) {
	projects := writeTranscript(t, "-work",
		userRecord(t, "<command-name>/compact</command-name>\n<command-message>compact</command-message>\n<command-args></command-args>"),
		userRecord(t, "<local-command-stdout>Compacted </local-command-stdout>"),
		userRecord(t, "carry on"),
	)

	result := read(t, projects, Request{Root: "/work", Last: -1})
	if result.TotalTurns != 2 {
		t.Fatalf("total turns = %d, want the command and the message: %+v", result.TotalTurns, result.Turns)
	}
	if result.Turns[0].Text != "/compact" {
		t.Fatalf("first turn = %q, want the command it ran", result.Turns[0].Text)
	}
	if result.Turns[1].Text != "carry on" {
		t.Fatalf("second turn = %q, want the next real message", result.Turns[1].Text)
	}
}

func TestOrdinaryMessagesAreNotMistakenForCommands(t *testing.T) {
	projects := writeTranscript(t, "-work",
		userRecord(t, "the <command-name> tag is what wraps a slash command"),
	)

	if got := read(t, projects, Request{Root: "/work", Last: -1}).Turns[0].Text; !strings.HasPrefix(got, "the ") {
		t.Fatalf("turn = %q, want prose left alone when the tag is not the prefix", got)
	}
}

func TestAnEmptySessionIDIsRejected(t *testing.T) {
	_, err := Reader{ClaudeProjects: t.TempDir()}.Read(Request{
		Runner: heikou.BackendClaude, SessionID: "  ", Root: "/work",
	})
	if err == nil {
		t.Fatal("an empty session id must be an error, not a scan of every project")
	}
}

// A transcript directory holding a name that is not a file must not stop the
// scan, and must never be opened.
func TestNonRegularCandidatesAreIgnored(t *testing.T) {
	projects := t.TempDir()
	decoy := filepath.Join(projects, "decoy", testSessionID+".jsonl")
	if err := os.MkdirAll(decoy, 0o755); err != nil {
		t.Fatalf("create decoy directory: %v", err)
	}
	real := filepath.Join(projects, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("create project directory: %v", err)
	}
	body := userRecord(t, "the real one") + "\n"
	if err := os.WriteFile(filepath.Join(real, testSessionID+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	result := read(t, projects, Request{Root: "/work", Last: -1})
	if result.Availability != Available || result.Turns[0].Text != "the real one" {
		t.Fatalf("result = %+v, want the directory entry skipped", result)
	}
}

func TestTranscriptEncodesToStableJSON(t *testing.T) {
	projects := writeTranscript(t, "-work",
		userRecord(t, "hi"), assistantRecord(t, textBlock("hello"), toolUseBlock("Bash")),
	)

	encoded, err := json.Marshal(read(t, projects, Request{Root: "/work", Last: -1}))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, want := range []string{
		`"runner":"claude"`, `"availability":"available"`, `"total_turns":2`,
		`"role":"assistant"`, `"tools":[{"name":"Bash","count":1}]`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("JSON is missing %s: %s", want, encoded)
		}
	}
}
