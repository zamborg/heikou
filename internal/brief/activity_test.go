package brief

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zamborg/heikou/internal/config"
	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/transcript"
)

const activitySessionID = "018f0000-0000-4000-8000-0000000000c1"

// writeActivityTranscript builds the Claude-shaped project directory the reader
// looks in, and returns the projects root to point a test reader at.
func writeActivityTranscript(t *testing.T, records ...map[string]any) string {
	t.Helper()
	projects := t.TempDir()
	writeTranscriptUnder(t, projects, records...)
	return projects
}

func writeTranscriptUnder(t *testing.T, projects string, records ...map[string]any) {
	t.Helper()
	directory := filepath.Join(projects, "-tmp-project")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("create project directory: %v", err)
	}
	var builder strings.Builder
	for _, value := range records {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("encode record: %v", err)
		}
		builder.Write(encoded)
		builder.WriteByte('\n')
	}
	path := filepath.Join(directory, activitySessionID+".jsonl")
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

func runningRecord(tool string, input map[string]any) map[string]any {
	return map[string]any{
		"type": "assistant", "timestamp": "2026-08-13T10:00:01Z",
		"message": map[string]any{"role": "assistant", "stop_reason": "tool_use", "content": []map[string]any{
			{"type": "tool_use", "id": "t1", "name": tool, "input": input},
		}},
	}
}

func activitySession(alive bool, backend heikou.Backend, activity time.Time) control.Session {
	status, runtimeStatus := control.StatusLive, heikou.StatusLive
	if !alive {
		status, runtimeStatus = control.StatusExited, heikou.StatusExited
	}
	runtime := heikou.Session{ID: activitySessionID, Status: runtimeStatus, LastActivityAt: activity}
	return control.Session{
		ID: activitySessionID, Backend: backend, Prompt: "task", Root: "/tmp/project",
		Status: status, Durable: true, Runtime: &runtime,
	}
}

func activityObserver(t *testing.T, projects string, now time.Time) *Observer {
	t.Helper()
	observer := NewObserver(config.Default().Brief)
	observer.reader = transcript.Reader{ClaudeProjects: projects}
	observer.now = func() time.Time { return now }
	return observer
}

func TestActivityIsPhrasedAsSomethingASessionIsDoing(t *testing.T) {
	now := mustTime(t, "2026-08-12T10:00:00Z")
	tests := map[string]struct {
		record map[string]any
		want   string
	}{
		"a shell call is what will run": {
			record: runningRecord("Bash", map[string]any{"command": "make check", "description": "Run the checks"}),
			want:   "running make check",
		},
		"an edit names the file, not its path": {
			record: runningRecord("Edit", map[string]any{"file_path": "/repo/internal/brief/observer.go"}),
			want:   "editing observer.go",
		},
		"a read says so": {
			record: runningRecord("Read", map[string]any{"file_path": "/repo/README.md"}),
			want:   "reading README.md",
		},
		"a search says what it is looking for": {
			record: runningRecord("Grep", map[string]any{"pattern": "BriefSource"}),
			want:   "searching for BriefSource",
		},
		"a delegated task says so": {
			record: runningRecord("Task", map[string]any{"description": "review the diff"}),
			want:   "delegating review the diff",
		},
		"an unfamiliar tool is named rather than given a wrong verb": {
			record: runningRecord("MysteryTool", map[string]any{"file_path": "/repo/main.go"}),
			want:   "MysteryTool /repo/main.go",
		},
		"an unfamiliar tool with nothing to name still says which one": {
			record: runningRecord("MysteryTool", map[string]any{"unknown": "value"}),
			want:   "running MysteryTool",
		},
		"a finished turn says what the session said": {
			record: map[string]any{
				"type": "assistant", "timestamp": "2026-08-13T10:00:01Z",
				"message": map[string]any{"role": "assistant", "stop_reason": "end_turn",
					"content": []map[string]any{{"type": "text", "text": "make check is green"}}},
			},
			want: "replied · make check is green",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			projects := writeActivityTranscript(t, test.record)
			observer := activityObserver(t, projects, now)
			session := activitySession(true, heikou.BackendClaude, now)
			observations, report := observer.Observe(t.Context(), []control.Session{session}, nil)
			if len(report.Failures) != 0 {
				t.Fatalf("failures: %v", report.Failures)
			}
			if got := observations[Key{Session: session.ID, Source: SourceActivity}].Text; got != test.want {
				t.Fatalf("activity line = %q, want %q", got, test.want)
			}
		})
	}
}

// The whole point of the cache is that drawing a row opens no files. A source
// asked for a fragment with nothing observed yet has to return nothing, and the
// slot falls through to the next source rather than rendering blank.
//
// The transcript here is written where a reader with no configured location
// would find it, and the home directory is redirected so that is this test's
// temporary one. So a fragment that came back with "running make check" would
// have gone to the filesystem on the render path, which is the failure this is
// looking for.
func TestActivityFragmentReadsOnlyTheCacheAndFallsThroughWhenEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	writeTranscriptUnder(t, filepath.Join(home, ".claude", "projects"),
		runningRecord("Bash", map[string]any{"command": "make check"}))
	reachable, err := (transcript.Reader{}).ReadActivity(transcript.Request{
		Runner: heikou.BackendClaude, SessionID: activitySessionID, Root: "/tmp/project",
	})
	if err != nil || !reachable.Known() {
		t.Fatalf("fixture is not where an unconfigured reader looks (%+v, %v); this test would pass for the wrong reason", reachable, err)
	}

	settings := config.Default().Brief
	session := activitySession(true, heikou.BackendClaude, time.Now())
	session.LastUserMessage = "also update the release notes"

	registry := NewRegistry(settings, nil)
	if fragment := registry[SourceActivity].Fragment(session); !fragment.Empty() {
		t.Fatalf("an unobserved activity source produced %q from the render path", fragment.Text)
	}
	item := LayoutFrom(settings).Resolve(session, registry)
	if item.Detail.Source != SourceLatest {
		t.Fatalf("detail = %+v; an empty activity source must fall through", item.Detail)
	}
}

// Derived text is the reason the approximate mark exists. A phrase assembled
// from another program's records is not something Heikou can defend as written,
// and it lands in the same columns as a title the user typed.
func TestObservedActivityIsNeverProven(t *testing.T) {
	settings := config.Default().Brief
	session := activitySession(true, heikou.BackendClaude, time.Now())
	observations := Observations{
		{Session: session.ID, Source: SourceActivity}: {Text: "running make check"},
	}
	fragment := LayoutFrom(settings).Resolve(session, NewRegistry(settings, observations)).Detail
	if fragment.Source != SourceActivity || fragment.Text != "running make check" {
		t.Fatalf("detail = %+v", fragment)
	}
	if fragment.Proven {
		t.Fatal("a phrase derived from a runner's records was reported as proven")
	}
	if fragment.Label() != "runner activity" {
		t.Fatalf("label = %q", fragment.Label())
	}
}

// Resolving a layout must not be able to block, whatever a source would do if
// asked to go and look. The bound is generous on purpose: it is not a benchmark,
// it is the difference between reading a map and opening a file per row.
func TestResolvingManyRowsDoesNoWork(t *testing.T) {
	settings := config.Default().Brief
	observations := Observations{
		{Session: activitySessionID, Source: SourceActivity}: {Text: "running make check"},
	}
	layout, registry := LayoutFrom(settings), NewRegistry(settings, observations)
	session := activitySession(true, heikou.BackendClaude, time.Now())

	start := time.Now()
	for range 10_000 {
		layout.Resolve(session, registry)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("10000 resolutions took %s; a render is doing more than reading a cache", elapsed)
	}
}

// A row shows the runtime state beside the brief. Keeping a line that says
// "running make check" next to "exited" is the same looks-current failure as
// freezing a failed source's text, so the observation goes with the session.
func TestActivityIsDroppedWhenASessionIsNoLongerAlive(t *testing.T) {
	now := mustTime(t, "2026-08-12T10:00:00Z")
	projects := writeActivityTranscript(t, runningRecord("Bash", map[string]any{"command": "make check"}))
	observer := activityObserver(t, projects, now)

	key := Key{Session: activitySessionID, Source: SourceActivity}
	observations, _ := observer.Observe(t.Context(), []control.Session{activitySession(true, heikou.BackendClaude, now)}, nil)
	if observations[key].Text == "" {
		t.Fatalf("a live session was not observed: %+v", observations)
	}

	observations, report := observer.Observe(t.Context(),
		[]control.Session{activitySession(false, heikou.BackendClaude, now)}, observations)
	if _, present := observations[key]; present {
		t.Fatal("an exited session kept a line saying what it was doing")
	}
	if report.Ran != 0 {
		t.Fatalf("an exited session was read anyway: %+v", report)
	}
}

// Codex records a rollout Heikou cannot identify, which is a normal state of the
// world rather than a failure. It must not be reported as a broken source, and
// it must not be retried every pass.
func TestAnUnreadableRunnerIsQuietRatherThanAFailure(t *testing.T) {
	now := mustTime(t, "2026-08-12T10:00:00Z")
	observer := activityObserver(t, t.TempDir(), now)
	session := activitySession(true, heikou.BackendCodex, now)

	observations, report := observer.Observe(t.Context(), []control.Session{session}, nil)
	if len(report.Failures) != 0 {
		t.Fatalf("an unsupported runner was reported as a failure: %v", report.Failures)
	}
	key := Key{Session: session.ID, Source: SourceActivity}
	if observation, present := observations[key]; !present || observation.Text != "" {
		t.Fatalf("observation = %+v; an empty answer still has to be recorded so it is not retried at once", observation)
	}
	if !NewRegistry(config.Default().Brief, observations)[SourceActivity].Fragment(session).Empty() {
		t.Fatal("an empty observation reached a row")
	}

	// Same interval rule as a command source: nothing re-reads until the
	// session has both waited and moved.
	observer.now = func() time.Time { return now.Add(time.Hour) }
	if _, report = observer.Observe(t.Context(), []control.Session{session}, observations); report.Ran != 0 {
		t.Fatalf("a quiet session was read again: %+v", report)
	}
}

// The activity source obeys the same two conditions as a command source, and
// the interval is the one this package sets rather than one from settings.
func TestActivityRereadsOnlyAfterTheIntervalAndSomeMovement(t *testing.T) {
	start := mustTime(t, "2026-08-12T10:00:00Z")
	projects := writeActivityTranscript(t, runningRecord("Bash", map[string]any{"command": "make check"}))
	observer := activityObserver(t, projects, start)

	observations, report := observer.Observe(t.Context(), []control.Session{activitySession(true, heikou.BackendClaude, start)}, nil)
	if report.Ran != 1 {
		t.Fatalf("first look = %+v", report)
	}

	observer.now = func() time.Time { return start.Add(activityInterval / 2) }
	moved := activitySession(true, heikou.BackendClaude, start.Add(activityInterval/2))
	if _, report = observer.Observe(t.Context(), []control.Session{moved}, observations); report.Ran != 0 {
		t.Fatalf("read again inside the interval: %+v", report)
	}

	observer.now = func() time.Time { return start.Add(2 * activityInterval) }
	if _, report = observer.Observe(t.Context(),
		[]control.Session{activitySession(true, heikou.BackendClaude, start)}, observations); report.Ran != 0 {
		t.Fatalf("read a session that had not moved: %+v", report)
	}
	if _, report = observer.Observe(t.Context(),
		[]control.Session{activitySession(true, heikou.BackendClaude, start.Add(time.Minute))}, observations); report.Ran != 1 {
		t.Fatalf("did not read a session that had waited and moved: %+v", report)
	}
}
