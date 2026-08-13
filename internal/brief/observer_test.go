package brief

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zamborg/heikou/internal/config"
	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/heikou"
)

func observerSession(id string, activity time.Time) control.Session {
	runtime := heikou.Session{ID: id, Status: heikou.StatusLive, LastActivityAt: activity}
	return control.Session{
		ID: id, Backend: heikou.BackendClaude, Prompt: "task", Root: "/tmp/project",
		Status: control.StatusLive, Durable: true, Runtime: &runtime,
	}
}

func testObserver(t *testing.T, now time.Time, run runFunc) *Observer {
	t.Helper()
	observer := NewObserver(config.BriefConfig{
		Lead:    []string{"status"},
		Sources: map[string]config.BriefSourceConfig{"status": {Command: []string{"status-line"}, IntervalSeconds: 10, TimeoutSeconds: 3}},
	})
	observer.run = run
	observer.now = func() time.Time { return now }
	return observer
}

func TestObserverRecordsWhatACommandPrinted(t *testing.T) {
	now := mustTime(t, "2026-08-12T10:00:00Z")
	observer := testObserver(t, now, func(context.Context, []string, []string) ([]byte, error) {
		return []byte("waiting for input\nsecond line is ignored\n"), nil
	})

	session := observerSession("018f0000-0000-4000-8000-000000000001", now)
	observations, report := observer.Observe(t.Context(), []control.Session{session}, nil)
	if report.Ran != 1 || len(report.Failures) != 0 {
		t.Fatalf("report = %+v", report)
	}
	observation := observations[Key{Session: session.ID, Source: "status"}]
	if observation.Text != "waiting for input" {
		t.Fatalf("observed text = %q; only the first line belongs in a row", observation.Text)
	}
	if !observation.ActivityAt.Equal(now) {
		t.Fatalf("observation did not record the activity it was taken at: %+v", observation)
	}
}

// A quiet session cannot have a different status line, so asking again spends a
// process to learn nothing. This is the whole cost control for the feature.
func TestObserverSkipsSessionsThatHaveNotMovedSinceTheLastLook(t *testing.T) {
	start := mustTime(t, "2026-08-12T10:00:00Z")
	var runs atomic.Int64
	observer := testObserver(t, start, func(context.Context, []string, []string) ([]byte, error) {
		runs.Add(1)
		return []byte("busy"), nil
	})

	activity := start
	session := observerSession("018f0000-0000-4000-8000-000000000001", activity)
	observations, _ := observer.Observe(t.Context(), []control.Session{session}, nil)
	if runs.Load() != 1 {
		t.Fatalf("first pass ran %d commands, want 1", runs.Load())
	}

	// Interval elapsed, but the session has been idle throughout.
	observer.now = func() time.Time { return start.Add(time.Minute) }
	observations, report := observer.Observe(t.Context(), []control.Session{session}, observations)
	if runs.Load() != 1 || report.Ran != 0 {
		t.Fatalf("an idle session was polled again: runs=%d report=%+v", runs.Load(), report)
	}
	if observations[Key{Session: session.ID, Source: "status"}].Text != "busy" {
		t.Fatal("skipping a quiet session dropped what it last reported")
	}

	// Now it does something.
	session = observerSession(session.ID, start.Add(90*time.Second))
	if _, report = observer.Observe(t.Context(), []control.Session{session}, observations); report.Ran != 1 {
		t.Fatalf("an active session was not re-observed: %+v", report)
	}
}

func TestObserverHoldsTheIntervalEvenWhenASessionIsBusy(t *testing.T) {
	start := mustTime(t, "2026-08-12T10:00:00Z")
	var runs atomic.Int64
	observer := testObserver(t, start, func(context.Context, []string, []string) ([]byte, error) {
		runs.Add(1)
		return []byte("busy"), nil
	})

	session := observerSession("018f0000-0000-4000-8000-000000000001", start)
	observations, _ := observer.Observe(t.Context(), []control.Session{session}, nil)

	observer.now = func() time.Time { return start.Add(2 * time.Second) }
	session = observerSession(session.ID, start.Add(2*time.Second))
	if _, report := observer.Observe(t.Context(), []control.Session{session}, observations); report.Ran != 0 {
		t.Fatalf("interval was not respected for an active session: %+v", report)
	}
	if runs.Load() != 1 {
		t.Fatalf("ran %d commands inside the interval, want 1", runs.Load())
	}
}

// Text that can no longer be refreshed is worse than no text, because it looks
// current. A failing source falls through to the next one in the slot.
func TestObserverDropsWhatAFailingSourceLastReported(t *testing.T) {
	start := mustTime(t, "2026-08-12T10:00:00Z")
	fail := false
	observer := testObserver(t, start, func(context.Context, []string, []string) ([]byte, error) {
		if fail {
			return nil, errors.New("exit status 1")
		}
		return []byte("waiting for input"), nil
	})

	session := observerSession("018f0000-0000-4000-8000-000000000001", start)
	observations, _ := observer.Observe(t.Context(), []control.Session{session}, nil)

	fail = true
	observer.now = func() time.Time { return start.Add(time.Minute) }
	session = observerSession(session.ID, start.Add(time.Minute))
	observations, report := observer.Observe(t.Context(), []control.Session{session}, observations)
	if len(report.Failures) != 1 {
		t.Fatalf("failure was not reported: %+v", report)
	}
	if _, present := observations[Key{Session: session.ID, Source: "status"}]; present {
		t.Fatal("a failed source kept stale text on screen")
	}
	if !strings.Contains(report.Failures[0].Error(), "018f00") {
		t.Fatalf("failure does not name the session: %v", report.Failures[0])
	}
}

func TestObserverForgetsSessionsThatNoLongerExist(t *testing.T) {
	start := mustTime(t, "2026-08-12T10:00:00Z")
	observer := testObserver(t, start, func(context.Context, []string, []string) ([]byte, error) {
		return []byte("busy"), nil
	})

	first := observerSession("018f0000-0000-4000-8000-000000000001", start)
	second := observerSession("018f0000-0000-4000-8000-000000000002", start)
	observations, _ := observer.Observe(t.Context(), []control.Session{first, second}, nil)
	if len(observations) != 2 {
		t.Fatalf("observed %d sessions, want 2", len(observations))
	}

	observations, _ = observer.Observe(t.Context(), []control.Session{first}, observations)
	if _, present := observations[Key{Session: second.ID, Source: "status"}]; present {
		t.Fatal("a deleted session's observation was retained forever")
	}
}

func TestObserverCapsOnePassAndSaysWhatItDeferred(t *testing.T) {
	start := mustTime(t, "2026-08-12T10:00:00Z")
	var runs atomic.Int64
	observer := testObserver(t, start, func(context.Context, []string, []string) ([]byte, error) {
		runs.Add(1)
		return []byte("busy"), nil
	})

	sessions := make([]control.Session, 0, maxRunsPerObservation+5)
	for index := range cap(sessions) {
		sessions = append(sessions, observerSession(fmt.Sprintf("018f0000-0000-4000-8000-%012d", index), start))
	}
	_, report := observer.Observe(t.Context(), sessions, nil)
	if report.Ran != maxRunsPerObservation {
		t.Fatalf("ran %d, want the cap of %d", report.Ran, maxRunsPerObservation)
	}
	if report.Deferred != 5 {
		t.Fatalf("deferred = %d, want 5; a silent cap reads as full coverage", report.Deferred)
	}
	if runs.Load() != int64(maxRunsPerObservation) {
		t.Fatalf("ran %d commands past the cap", runs.Load())
	}
}

func TestObserverBoundsHowManyCommandsRunAtOnce(t *testing.T) {
	start := mustTime(t, "2026-08-12T10:00:00Z")
	var mutex sync.Mutex
	current, peak := 0, 0
	observer := testObserver(t, start, func(context.Context, []string, []string) ([]byte, error) {
		mutex.Lock()
		current++
		peak = max(peak, current)
		mutex.Unlock()
		time.Sleep(2 * time.Millisecond)
		mutex.Lock()
		current--
		mutex.Unlock()
		return []byte("busy"), nil
	})

	sessions := make([]control.Session, 0, 20)
	for index := range cap(sessions) {
		sessions = append(sessions, observerSession(fmt.Sprintf("018f0000-0000-4000-8000-%012d", index), start))
	}
	observer.Observe(t.Context(), sessions, nil)

	mutex.Lock()
	defer mutex.Unlock()
	if peak > maxConcurrentRuns {
		t.Fatalf("%d commands ran at once, cap is %d", peak, maxConcurrentRuns)
	}
}

func TestObserverRejectsOutputThatIsNotUTF8(t *testing.T) {
	start := mustTime(t, "2026-08-12T10:00:00Z")
	observer := testObserver(t, start, func(context.Context, []string, []string) ([]byte, error) {
		return []byte{0xff, 0xfe, 0x00}, nil
	})
	session := observerSession("018f0000-0000-4000-8000-000000000001", start)
	observations, report := observer.Observe(t.Context(), []control.Session{session}, nil)
	if len(report.Failures) != 1 {
		t.Fatalf("invalid UTF-8 was accepted: %+v", report)
	}
	if len(observations) != 0 {
		t.Fatalf("invalid output was stored: %+v", observations)
	}
}

// A brief source is a reason to describe a session, not a reason to hand what
// someone typed to another program on a timer.
func TestSessionEnvironmentWithholdsPromptsAndMessages(t *testing.T) {
	session := observerSession("018f0000-0000-4000-8000-000000000001", time.Now())
	session.Prompt = "SECRET-PROMPT"
	session.LastUserMessage = "SECRET-MESSAGE"
	session.Record.Title = "OAuth work"

	environment := sessionEnvironment(session)
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "SECRET-PROMPT") || strings.Contains(joined, "SECRET-MESSAGE") {
		t.Fatalf("session content leaked into a brief source's environment:\n%s", joined)
	}
	for _, want := range []string{
		"HEIKOU_SESSION_ID=018f0000-0000-4000-8000-000000000001",
		"HEIKOU_SESSION_RUNNER=claude",
		"HEIKOU_SESSION_STATE=live",
		"HEIKOU_SESSION_ROOT=/tmp/project",
		"HEIKOU_SESSION_TITLE=OAuth work",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("environment is missing %q", want)
		}
	}
}

func TestUnconfiguredObserverRunsNothing(t *testing.T) {
	observer := NewObserver(config.Default().Brief)
	if observer.Configured() {
		t.Fatal("the default configuration reported having sources to run")
	}
	observations, report := observer.Observe(t.Context(), []control.Session{observerSession("x", time.Now())}, nil)
	if len(observations) != 0 || report.Ran != 0 {
		t.Fatalf("an unconfigured observer did work: %+v %+v", observations, report)
	}
}

// A command that never stops printing must not be able to grow the dashboard's
// memory without bound.
func TestBoundedBufferAcceptsEverythingAndKeepsOnlyItsLimit(t *testing.T) {
	buffer := boundedBuffer{limit: 16}
	written, err := buffer.Write([]byte(strings.Repeat("a", 1000)))
	if err != nil {
		t.Fatalf("bounded write returned an error the child would see as EPIPE: %v", err)
	}
	if written != 1000 {
		t.Fatalf("reported %d bytes written, want 1000", written)
	}
	if buffer.buffer.Len() != 16 {
		t.Fatalf("kept %d bytes, want the 16-byte limit", buffer.buffer.Len())
	}
}

func TestRunCommandCapturesStdoutAndSurfacesFailures(t *testing.T) {
	output, err := runCommand(t.Context(), []string{"printf", "hello\n"}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.TrimSpace(string(output)) != "hello" {
		t.Fatalf("stdout = %q", output)
	}
	if _, err := runCommand(t.Context(), []string{"sh", "-c", "echo boom >&2; exit 3"}, nil); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Fatalf("stderr was not surfaced in the failure: %v", err)
	}
}

// Everything above stubs the process runner. This one goes through the real
// one, so the environment contract and the output path are proven end to end
// rather than only where the fake meets them.
func TestObserverRunsARealCommandAndUsesItsOutput(t *testing.T) {
	observer := NewObserver(config.BriefConfig{
		Lead: []string{"status"},
		Sources: map[string]config.BriefSourceConfig{
			"status": {Command: []string{"sh", "-c", `printf '%s is %s\n' "$HEIKOU_SESSION_RUNNER" "$HEIKOU_SESSION_STATE"`},
				IntervalSeconds: 10, TimeoutSeconds: 5},
		},
	})

	session := observerSession("018f0000-0000-4000-8000-000000000001", time.Now())
	observations, report := observer.Observe(t.Context(), []control.Session{session}, nil)
	if len(report.Failures) != 0 {
		t.Fatalf("real command failed: %v", report.Failures)
	}
	if got := observations[Key{Session: session.ID, Source: "status"}].Text; got != "claude is live" {
		t.Fatalf("observed text = %q, want the session described through its environment", got)
	}
}

func TestObserverTimesOutASlowCommand(t *testing.T) {
	observer := NewObserver(config.BriefConfig{
		Lead: []string{"status"},
		Sources: map[string]config.BriefSourceConfig{
			"status": {Command: []string{"sleep", "30"}, IntervalSeconds: 1, TimeoutSeconds: 1},
		},
	})

	session := observerSession("018f0000-0000-4000-8000-000000000001", time.Now())
	start := time.Now()
	observations, report := observer.Observe(t.Context(), []control.Session{session}, nil)
	if len(report.Failures) != 1 {
		t.Fatalf("a hung command was not reported as a failure: %+v", report)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("timeout did not bound the run: %s", elapsed)
	}
	if len(observations) != 0 {
		t.Fatalf("a timed-out command left text behind: %+v", observations)
	}
}
