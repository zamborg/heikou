package main

// These tests drive the command handlers directly, which the end-to-end suite
// in e2e_test.go cannot do: it builds the binary and runs it, so it needs tmux,
// a real home directory, and a process per assertion. Both layers earn their
// place. The end-to-end suite proves the thing that ships works; these prove
// the argument handling, the refusal wording and the shape of --json, on every
// verb, without a tmux server anywhere in sight.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zamborg/heikou/internal/config"
	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/control/controltest"
	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/transcript"
	"github.com/zamborg/heikou/internal/workstream"
)

const (
	testSessionID    = "018f0000-0000-4000-8000-0000000000a1"
	testWorkstreamID = "018f0000-0000-4000-8000-0000000000b2"
)

// harness runs a verb in-process and records everything it did to the world
// outside itself.
type harness struct {
	out   bytes.Buffer
	errOu bytes.Buffer
	// dials counts how many times the verb reached for a controller. A verb
	// that refuses its arguments must never reach for one.
	dials   int
	service *controltest.Stub
	app     *app
	// claudeProjects is the fixture directory the history verb reads.
	claudeProjects string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{service: populatedService(), claudeProjects: t.TempDir()}
	h.app = &app{
		out:      &h.out,
		err:      &h.errOu,
		settings: func() (config.Store, config.Config, error) { return config.Store{}, config.Default(), nil },
		workdir:  func() string { return t.TempDir() },
		dial: func(string) (control.Service, error) {
			h.dials++
			return h.service, nil
		},
		// Pointed at a temporary directory so a developer's own transcripts are
		// never what a test read.
		transcripts: transcript.Reader{ClaudeProjects: h.claudeProjects},
	}
	return h
}

// writeTranscript files a Claude-shaped transcript for the harness session.
func (h *harness) writeTranscript(t *testing.T, lines ...string) {
	t.Helper()
	directory := filepath.Join(h.claudeProjects, "-tmp-project")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("create project directory: %v", err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(directory, testSessionID+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

// populatedService answers with one workstream and one session, which is enough
// for every verb to find what it was asked about.
func populatedService() *controltest.Stub {
	created := time.Now().Add(-90 * time.Minute)
	session := control.Session{
		ID: testSessionID, Backend: heikou.BackendClaude, Prompt: "ship the parser",
		Root: "/tmp/project", CreatedAt: created, WorkstreamID: testWorkstreamID,
		Status: control.StatusLive, Durable: true,
	}
	item := workstream.Workstream{
		ID: testWorkstreamID, Name: "Parser", ArtifactDir: "/tmp/artifacts/parser",
		Roots: []string{"/tmp/project"}, Revision: 3,
	}
	return &controltest.Stub{
		SnapshotFunc: func(context.Context) (control.Snapshot, error) {
			return control.Snapshot{
				Revision: 7, Workstreams: []workstream.Workstream{item},
				Sessions: []control.Session{session}, StatePath: "/tmp/state.json",
			}, nil
		},
		FindFunc: func(context.Context, string) (control.Session, error) { return session, nil },
		CreateWorkstreamFunc: func(context.Context, string, string, []string) (workstream.Workstream, error) {
			return item, nil
		},
		CaptureFunc: func(context.Context, string, int) (string, error) { return "frame contents", nil },
	}
}

// A verb that refuses its arguments must fail on the arguments, not on the
// environment. Before the handlers took a dialer, several of them built a
// controller — and therefore required a working tmux — before deciding whether
// the command made sense at all. That turned "you forgot --yes" into "tmux is
// required" on any machine where tmux was missing or the server was wedged.
func TestRefusalsNeverReachForAController(t *testing.T) {
	for _, testCase := range []struct {
		name string
		args []string
		want string
	}{
		{"list rejects a positional", []string{"list", "stray"}, "usage: h list"},
		{"spawn requires a task", []string{"spawn"}, "usage: h spawn"},
		{"send requires a message", []string{"send", testSessionID}, "usage: h send"},
		{"attach takes exactly one session", []string{"attach"}, "usage: h attach"},
		{"stop takes exactly one session", []string{"stop", "a", "b"}, "usage: h stop"},
		{"peek takes exactly one session", []string{"peek"}, "usage: h peek"},
		{"history takes exactly one session", []string{"history"}, "usage: h history"},
		{"history refuses a negative count", []string{"history", testSessionID, "--last", "-1"}, "cannot be negative"},
		{"delete demands confirmation", []string{"delete", testSessionID}, "pass --yes to confirm"},
		{"archive demands confirmation", []string{"ws", "archive", "Parser"}, "pass --yes to confirm"},
		{"title refuses an empty title", []string{"title", testSessionID}, "usage: h title"},
		{"title refuses two intents at once", []string{"title", testSessionID, "--clear", "new"}, "not both"},
		{"move demands a destination", []string{"move", testSessionID}, "usage: h move"},
		{"move refuses two destinations", []string{"move", testSessionID, "-w", "Parser", "--ungrouped"}, "usage: h move"},
		{"ws create requires a name", []string{"ws", "create"}, "usage: h ws create"},
		{"ws rename requires a new name", []string{"ws", "rename", "Parser"}, "usage: h ws rename"},
		{"ws reorder requires a direction", []string{"ws", "reorder", "Parser"}, "--up|--down"},
		{"ws reorder refuses both directions", []string{"ws", "reorder", "Parser", "--up", "--down"}, "--up|--down"},
		{"ws root rejects an unknown action", []string{"ws", "root", "flip", "Parser", "/tmp"}, "want add, set, or rm"},
		{"ws root add needs a directory", []string{"ws", "root", "add", "Parser"}, "usage: h ws root add"},
		{"an unknown verb names the way out", []string{"frobnicate"}, "run h help"},
		{"an unknown ws verb names the way out", []string{"ws", "frobnicate"}, "run h help"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			h := newHarness(t)
			err := h.app.run(testCase.args)
			if err == nil {
				t.Fatalf("h %s succeeded; expected a refusal", strings.Join(testCase.args, " "))
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("refusal = %q, want it to contain %q", err.Error(), testCase.want)
			}
			if h.dials != 0 {
				t.Errorf("h %s built a controller before refusing; a bad argument must not need tmux",
					strings.Join(testCase.args, " "))
			}
			if h.out.Len() != 0 {
				t.Errorf("a refused command wrote %q to stdout; refusals belong in the error", h.out.String())
			}
		})
	}
}

// Every verb that takes a positional parses through parseAnywhere. The
// end-to-end suite proves this for spawn; this proves it for the rest, where a
// silently swallowed flag would be just as wrong and far less obvious.
func TestFlagsAreAcceptedAfterPositionals(t *testing.T) {
	for _, testCase := range []struct {
		name string
		args []string
	}{
		{"ws create", []string{"ws", "create", "API work", "-C", "/tmp/api"}},
		{"ws rename", []string{"ws", "rename", "Parser", "Lexer", "--json"}},
		{"ws archive", []string{"ws", "archive", "Parser", "--yes"}},
		{"ws reorder", []string{"ws", "reorder", "Parser", "--up"}},
		{"ws root add", []string{"ws", "root", "add", "Parser", "/tmp/extra", "--json"}},
		{"title", []string{"title", testSessionID, "a new title", "--json"}},
		{"move", []string{"move", testSessionID, "--ungrouped"}},
		{"adopt", []string{"adopt", testSessionID, "-w", "Parser"}},
		{"delete", []string{"delete", testSessionID, "--yes"}},
		{"peek", []string{"peek", testSessionID, "--lines", "10"}},
		{"history", []string{"history", testSessionID, "--last", "5"}},
		{"send", []string{"send", testSessionID, "carry on", "--json"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			h := newHarness(t)
			if err := h.app.run(testCase.args); err != nil {
				t.Fatalf("h %s: %v", strings.Join(testCase.args, " "), err)
			}
			if h.dials == 0 {
				t.Error("the command succeeded without reaching the controller, so it did nothing")
			}
		})
	}
}

// A --json surface exists so a pilot can read it. Every one of these is checked
// for the keys the pilot is told to look at, because a renamed key does not
// crash the pilot — it stops finding the data and starts guessing.
func TestJSONResultsCarryTheKeysThePilotReads(t *testing.T) {
	for _, testCase := range []struct {
		name string
		args []string
		keys []string
	}{
		{"ws create", []string{"ws", "create", "API work", "--json"},
			[]string{"status", "workstream_id", "name", "roots", "artifact_dir"}},
		{"ws rename", []string{"ws", "rename", "Parser", "Lexer", "--json"},
			[]string{"status", "workstream_id", "name"}},
		{"ws reorder", []string{"ws", "reorder", "Parser", "--up", "--json"},
			[]string{"status", "workstream_id", "moved"}},
		{"ws archive", []string{"ws", "archive", "Parser", "--yes", "--json"},
			[]string{"status", "workstream_id"}},
		{"ws root add", []string{"ws", "root", "add", "Parser", "/tmp/extra", "--json"},
			[]string{"status", "workstream_id", "root"}},
		{"title", []string{"title", testSessionID, "renamed", "--json"},
			[]string{"status", "session_id", "title"}},
		{"move", []string{"move", testSessionID, "--ungrouped", "--json"},
			[]string{"status", "session_id", "workstream_id"}},
		{"adopt", []string{"adopt", testSessionID, "--json"},
			[]string{"status", "session_id", "workstream_id"}},
		{"delete", []string{"delete", testSessionID, "--yes", "--json"},
			[]string{"status", "session_id"}},
		{"send", []string{"send", testSessionID, "carry on", "--json"},
			[]string{"session_id", "status"}},
		{"peek", []string{"peek", testSessionID, "--json"},
			[]string{"session_id", "state", "capture", "capture_is_current_frame_only"}},
		{"history", []string{"history", testSessionID, "--json"},
			[]string{"session_id", "runner", "availability", "total_turns", "turns"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			h := newHarness(t)
			if err := h.app.run(testCase.args); err != nil {
				t.Fatalf("h %s: %v", strings.Join(testCase.args, " "), err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(h.out.Bytes(), &decoded); err != nil {
				t.Fatalf("--json output is not an object: %v (output %q)", err, h.out.String())
			}
			for _, key := range testCase.keys {
				if _, present := decoded[key]; !present {
					t.Errorf("--json result is missing %q; got keys %v", key, sortedKeys(decoded))
				}
			}
		})
	}
}

// peek is the only surface that hands a pilot raw pane output, and the flag
// that says so is the difference between a pilot quoting a frame and a pilot
// claiming to have read a conversation.
func TestPeekAlwaysLabelsItsCaptureAsAFrame(t *testing.T) {
	h := newHarness(t)
	if err := h.app.run([]string{"peek", testSessionID, "--json"}); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Capture     string `json:"capture"`
		FrameOnly   bool   `json:"capture_is_current_frame_only"`
		SessionID   string `json:"session_id"`
		StateString string `json:"state"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.FrameOnly {
		t.Error("peek did not mark its capture as a current frame")
	}
	if decoded.Capture != "frame contents" {
		t.Errorf("capture = %q, want the pane contents", decoded.Capture)
	}
}

// h list --json is the pilot's main read. Its top level is a snapshot object,
// not the per-command result the other verbs return.
func TestListJSONReportsTheWholeSnapshot(t *testing.T) {
	h := newHarness(t)
	if err := h.app.run([]string{"list", "--json"}); err != nil {
		t.Fatal(err)
	}
	var snapshot cliSnapshotJSON
	if err := json.Unmarshal(h.out.Bytes(), &snapshot); err != nil {
		t.Fatalf("h list --json is not a snapshot: %v", err)
	}
	if snapshot.Revision != 7 {
		t.Errorf("revision = %d, want 7", snapshot.Revision)
	}
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].ID != testSessionID {
		t.Fatalf("sessions = %+v, want the one session", snapshot.Sessions)
	}
	if got := snapshot.Sessions[0].Workstream; got != "Parser" {
		t.Errorf("workstream name = %q, want the resolved name rather than an id", got)
	}
	if len(snapshot.Workstreams) != 1 || snapshot.Workstreams[0].ArtifactDir == "" {
		t.Errorf("workstreams = %+v, want one carrying its artifact directory", snapshot.Workstreams)
	}
}

// The table is what a person reads. It must name the session, its state and its
// workstream, and it must not leak an ANSI escape from a pane into the terminal
// that is drawing the table.
func TestListTableNamesTheSessionAndItsGroup(t *testing.T) {
	h := newHarness(t)
	if err := h.app.run([]string{"list"}); err != nil {
		t.Fatal(err)
	}
	output := h.out.String()
	for _, want := range []string{"018f00", "claude", "live", "Parser", "ship the parser"} {
		if !strings.Contains(output, want) {
			t.Errorf("h list output is missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "\x1b") {
		t.Error("h list emitted an escape sequence")
	}
}

// A controller that is unreachable must say so rather than reporting success,
// and it must say so through the error rather than by printing to stdout.
func TestAnUnreachableControllerFailsTheCommand(t *testing.T) {
	h := newHarness(t)
	h.app.dial = func(string) (control.Service, error) {
		h.dials++
		return nil, errors.New("tmux is required: exec: \"tmux\": executable file not found in $PATH")
	}
	err := h.app.run([]string{"list"})
	if err == nil {
		t.Fatal("h list succeeded without a controller")
	}
	if !strings.Contains(err.Error(), "tmux is required") {
		t.Errorf("error = %q, want it to name the missing dependency", err)
	}
	if h.out.Len() != 0 {
		t.Errorf("a failed command wrote %q to stdout", h.out.String())
	}
}

// An error from the controller is the user's answer. Wrapping it away, or
// printing a success line before checking it, would report a mutation that
// never happened.
func TestAControllerRefusalReachesTheUserUnchanged(t *testing.T) {
	h := newHarness(t)
	h.service.DeleteSessionFunc = func(context.Context, string) error {
		return errors.New("cannot delete session while its tmux runtime exists; stop it first")
	}
	err := h.app.run([]string{"delete", testSessionID, "--yes"})
	if err == nil {
		t.Fatal("delete succeeded despite the controller refusing")
	}
	if !strings.Contains(err.Error(), "stop it first") {
		t.Errorf("error = %q, want the controller's own wording including the remedy", err)
	}
	if h.out.Len() != 0 {
		t.Errorf("delete printed %q before the refusal was checked", h.out.String())
	}
}

// The handlers write through the injected writer. Anything reaching os.Stdout
// directly would be invisible here and unroutable anywhere else.
func TestVerbsWriteOnlyThroughTheInjectedWriter(t *testing.T) {
	for _, args := range [][]string{
		{"list"},
		{"ws", "list"},
		{"title", testSessionID, "a title"},
		{"move", testSessionID, "--ungrouped"},
		{"stop", testSessionID},
		{"send", testSessionID, "hello"},
	} {
		h := newHarness(t)
		if err := h.app.run(args); err != nil {
			t.Fatalf("h %s: %v", strings.Join(args, " "), err)
		}
		if h.out.Len() == 0 {
			t.Errorf("h %s produced no output on the injected writer", strings.Join(args, " "))
		}
	}
}

// Empty state is a normal state, not an error, and it should say what to do
// next rather than printing an empty table.
func TestEmptyStateExplainsItselfRatherThanPrintingNothing(t *testing.T) {
	for _, testCase := range []struct {
		args []string
		want string
	}{
		{[]string{"list"}, "no heikou sessions"},
		{[]string{"ws", "list"}, "h ws create"},
	} {
		h := newHarness(t)
		h.service.SnapshotFunc = func(context.Context) (control.Snapshot, error) {
			return control.Snapshot{}, nil
		}
		if err := h.app.run(testCase.args); err != nil {
			t.Fatalf("h %s: %v", strings.Join(testCase.args, " "), err)
		}
		if !strings.Contains(h.out.String(), testCase.want) {
			t.Errorf("h %s on empty state = %q, want it to mention %q",
				strings.Join(testCase.args, " "), h.out.String(), testCase.want)
		}
	}
}

// spawn resolves -w through the same matcher every other surface uses, so an
// ambiguous or unknown name has to fail rather than pick one.
func TestSpawnRefusesAWorkstreamItCannotResolve(t *testing.T) {
	h := newHarness(t)
	h.service.SnapshotFunc = func(context.Context) (control.Snapshot, error) {
		return control.Snapshot{Workstreams: []workstream.Workstream{
			{ID: "018f0000-0000-4000-8000-0000000000c1", Name: "Parser frontend"},
			{ID: "018f0000-0000-4000-8000-0000000000c2", Name: "Parser backend"},
		}}, nil
	}
	err := h.app.run([]string{"spawn", "a task", "-w", "Parser"})
	if err == nil {
		t.Fatal("spawn picked one of two matching workstreams")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error = %q, want it to say the query is ambiguous", err)
	}
}

// The flags a spawn is given decide which agent runs where. Folding one into
// the prompt starts the wrong runner in the wrong directory, which is the bug
// parseAnywhere exists to prevent.
func TestSpawnCarriesEveryFlagIntoTheStartRequest(t *testing.T) {
	h := newHarness(t)
	var captured control.StartRequest
	h.service.StartFunc = func(_ context.Context, request control.StartRequest) (control.Session, error) {
		captured = request
		return control.Session{ID: testSessionID, Backend: request.Backend}, nil
	}
	if err := h.app.run([]string{"spawn", "ship the parser", "-r", "no-agent", "-C", "/tmp/elsewhere", "-w", "Parser"}); err != nil {
		t.Fatal(err)
	}
	if captured.Prompt != "ship the parser" {
		t.Errorf("prompt = %q, want the task alone with no flags folded in", captured.Prompt)
	}
	if captured.Backend != heikou.BackendNoAgent {
		t.Errorf("runner = %q, want the one -r asked for", captured.Backend)
	}
	if captured.Root != "/tmp/elsewhere" {
		t.Errorf("root = %q, want the one -C asked for", captured.Root)
	}
	if captured.WorkstreamID != testWorkstreamID {
		t.Errorf("workstream = %q, want the id -w resolved to", captured.WorkstreamID)
	}
}

// A title that begins with a dash is ordinary text a person might write. It is
// reachable through the documented terminator and, without it, must fail
// loudly rather than be read as a flag.
func TestADashLeadingTitleNeedsTheDocumentedTerminator(t *testing.T) {
	h := newHarness(t)
	var recorded string
	h.service.SetSessionTitleFunc = func(_ context.Context, _, title string) error {
		recorded = title
		return nil
	}
	if err := h.app.run([]string{"title", testSessionID, "--", "-w is not a flag here"}); err != nil {
		t.Fatal(err)
	}
	if recorded != "-w is not a flag here" {
		t.Errorf("title = %q, want the text after the terminator verbatim", recorded)
	}

	unescaped := newHarness(t)
	if err := unescaped.app.run([]string{"title", testSessionID, "-w is not a flag here"}); err == nil {
		t.Error("an unescaped dash-leading title was accepted; it must fail and name the fix")
	}
}

func sortedKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	return keys
}

// history and peek answer different questions and must never blur. Peek is the
// pane's current frame; history is what the runner recorded happened.
func TestHistoryReportsTurnsRatherThanTerminalOutput(t *testing.T) {
	h := newHarness(t)
	h.writeTranscript(t,
		`{"type":"user","timestamp":"2026-08-13T10:00:00Z","message":{"role":"user","content":"add the retry"}}`,
		`{"type":"assistant","timestamp":"2026-08-13T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"done"},{"type":"tool_use","name":"Edit"},{"type":"tool_use","name":"Edit"}]}}`,
	)
	if err := h.app.run([]string{"history", testSessionID}); err != nil {
		t.Fatal(err)
	}
	output := h.out.String()
	for _, want := range []string{"claude transcript", "2 turns", "add the retry", "done", "ran Edit ×2"} {
		if !strings.Contains(output, want) {
			t.Errorf("history output is missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "frame contents") {
		t.Error("history printed the captured pane; that is what peek is for")
	}
}

// A session with no transcript is a normal answer. Exiting non-zero would make
// a pilot treat a runner's file layout as an operational failure.
func TestHistoryWithoutATranscriptSucceedsAndSaysWhy(t *testing.T) {
	h := newHarness(t)
	if err := h.app.run([]string{"history", testSessionID}); err != nil {
		t.Fatalf("a missing transcript must not be an error: %v", err)
	}
	if !strings.Contains(h.out.String(), "no transcript") {
		t.Errorf("output = %q, want a plain statement that there is none", h.out.String())
	}
}

// Codex records rollouts under an id it mints itself, so Heikou cannot say
// which file belongs to this session. That is a different answer from "none
// was written", and --json has to let a caller tell them apart.
func TestHistoryDistinguishesAnUnsupportedRunnerFromAMissingFile(t *testing.T) {
	h := newHarness(t)
	h.service.FindFunc = func(context.Context, string) (control.Session, error) {
		return control.Session{
			ID: testSessionID, Backend: heikou.BackendCodex, Root: "/tmp/project",
			Status: control.StatusLive, Durable: true,
		}, nil
	}
	if err := h.app.run([]string{"history", testSessionID, "--json"}); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &decoded); err != nil {
		t.Fatalf("--json output is not an object: %v", err)
	}
	if decoded["availability"] != "unsupported" {
		t.Errorf("availability = %v, want unsupported", decoded["availability"])
	}
	if decoded["runner"] != "codex" {
		t.Errorf("runner = %v, want the answer to name what supplied it", decoded["runner"])
	}
}

// --last is a reading length. It must never change what the verb reports the
// session actually contains.
func TestHistoryLastTrimsTheOutputWithoutChangingTheCount(t *testing.T) {
	h := newHarness(t)
	lines := make([]string, 0, 6)
	for _, text := range []string{"one", "two", "three", "four", "five", "six"} {
		lines = append(lines, `{"type":"user","timestamp":"2026-08-13T10:00:00Z","message":{"role":"user","content":"`+text+`"}}`)
	}
	h.writeTranscript(t, lines...)
	if err := h.app.run([]string{"history", testSessionID, "--last", "2"}); err != nil {
		t.Fatal(err)
	}
	output := h.out.String()
	if !strings.Contains(output, "turns 5-6 of 6") {
		t.Errorf("output does not say where in the transcript it is:\n%s", output)
	}
	if strings.Contains(output, "one") {
		t.Errorf("--last 2 printed an older turn:\n%s", output)
	}
}
