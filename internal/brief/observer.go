package brief

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zamborg/heikou/internal/config"
	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/env"
	"github.com/zamborg/heikou/internal/format"
)

const (
	// maxConcurrentRuns bounds how many configured commands run at once. A
	// dashboard with twenty sessions and two sources should not become forty
	// simultaneous processes.
	maxConcurrentRuns = 4
	// maxRunsPerObservation bounds one pass. Whatever is left over is simply due
	// again next time; the Report says how much was deferred, because a cap that
	// reports nothing reads exactly like having covered everything.
	maxRunsPerObservation = 32
	// maxOutputBytes is read from a command before the rest is discarded.
	maxOutputBytes = 64 << 10
)

// Observer runs the configured command sources on its own cadence and returns
// what they reported. Nothing here is called from the render path.
type Observer struct {
	sources map[SourceID]config.BriefSourceConfig
	run     runFunc
	now     func() time.Time
}

// runFunc executes one command and returns its standard output. It is a field
// so tests can exercise scheduling, change detection, and sanitization without
// spawning processes.
type runFunc func(ctx context.Context, argv, environment []string) ([]byte, error)

// Report describes what one pass did that the user might need to know about.
type Report struct {
	Ran      int
	Deferred int
	Failures []error
}

func NewObserver(settings config.BriefConfig) *Observer {
	sources := make(map[SourceID]config.BriefSourceConfig, len(settings.Sources))
	for name, source := range settings.Sources {
		sources[SourceID(name)] = source
	}
	return &Observer{sources: sources, run: runCommand, now: time.Now}
}

// Configured reports whether anything would run at all, so the dashboard can
// skip scheduling entirely on the overwhelmingly common default configuration.
func (o *Observer) Configured() bool { return o != nil && len(o.sources) > 0 }

type pendingRun struct {
	key      Key
	session  control.Session
	settings config.BriefSourceConfig
}

// Observe runs every source that is due and returns the next set of
// observations. The result is a fresh map: entries for sessions or sources that
// no longer exist are dropped rather than accumulating forever.
//
// A source is due for a session when it has never been observed, or when the
// interval has elapsed *and* the session has shown terminal activity since the
// last observation. The activity condition is what keeps an idle dashboard
// free: a session that has done nothing cannot have a different status line, so
// asking again would spend a process to learn nothing.
func (o *Observer) Observe(ctx context.Context, sessions []control.Session, previous Observations) (Observations, Report) {
	next := make(Observations, len(previous))
	var report Report
	if !o.Configured() {
		return next, report
	}

	now := o.now()
	var due []pendingRun
	for _, session := range sessions {
		for id, settings := range o.sources {
			key := Key{Session: session.ID, Source: id}
			observation, seen := previous[key]
			if seen {
				next[key] = observation
			}
			if !isDue(observation, seen, session, settings, now) {
				continue
			}
			due = append(due, pendingRun{key: key, session: session, settings: settings})
		}
	}

	// Deterministic order so a cap always defers the same tail rather than an
	// arbitrary one, and so tests can assert what ran.
	sort.Slice(due, func(left, right int) bool {
		if due[left].key.Session != due[right].key.Session {
			return due[left].key.Session < due[right].key.Session
		}
		return due[left].key.Source < due[right].key.Source
	})
	if len(due) > maxRunsPerObservation {
		report.Deferred = len(due) - maxRunsPerObservation
		due = due[:maxRunsPerObservation]
	}
	report.Ran = len(due)

	type outcome struct {
		key         Key
		observation Observation
		err         error
	}
	outcomes := make([]outcome, len(due))
	var group sync.WaitGroup
	slots := make(chan struct{}, maxConcurrentRuns)
	for index, run := range due {
		group.Add(1)
		go func() {
			defer group.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			text, err := o.observeOne(ctx, run)
			outcomes[index] = outcome{
				key: run.key,
				observation: Observation{
					Text: text, ObservedAt: now, ActivityAt: run.session.LastActivity(),
				},
				err: err,
			}
		}()
	}
	group.Wait()

	for _, result := range outcomes {
		if result.err != nil {
			// A failed source falls through to the next one in the slot rather
			// than freezing whatever it said last. Text that can no longer be
			// refreshed is worse than no text: it looks current.
			delete(next, result.key)
			report.Failures = append(report.Failures, result.err)
			continue
		}
		next[result.key] = result.observation
	}
	return next, report
}

func isDue(observation Observation, seen bool, session control.Session, settings config.BriefSourceConfig, now time.Time) bool {
	if !seen {
		return true
	}
	if now.Sub(observation.ObservedAt) < time.Duration(settings.IntervalSeconds)*time.Second {
		return false
	}
	return !session.LastActivity().Equal(observation.ActivityAt)
}

func (o *Observer) observeOne(ctx context.Context, run pendingRun) (string, error) {
	timeout := time.Duration(run.settings.TimeoutSeconds) * time.Second
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	output, err := o.run(runCtx, run.settings.Command, sessionEnvironment(run.session))
	if err != nil {
		return "", fmt.Errorf("brief source %q for session %s: %w", run.key.Source, format.ShortID(run.key.Session), err)
	}
	if !utf8.Valid(output) {
		return "", fmt.Errorf("brief source %q for session %s: output is not valid UTF-8", run.key.Source, format.ShortID(run.key.Session))
	}
	line, _, _ := bytes.Cut(output, []byte("\n"))
	return briefText(string(line)), nil
}

// sessionEnvironment describes one session to a command.
//
// The prompt and the latest message are deliberately absent. They are the
// user's content, and wanting a status line in a row is not a reason to hand
// what someone typed to another program on a timer. The same rule keeps prompts
// and messages out of the diagnostic log.
func sessionEnvironment(session control.Session) []string {
	return append(os.Environ(),
		env.SessionID+"="+session.ID,
		env.SessionRunner+"="+string(session.Backend),
		env.SessionState+"="+string(session.Status),
		env.SessionRoot+"="+session.Root,
		env.SessionTitle+"="+session.Record.Title,
	)
}

func runCommand(ctx context.Context, argv, environment []string) ([]byte, error) {
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Env = environment
	command.Stdin = nil
	var stdout, stderr boundedBuffer
	stdout.limit, stderr.limit = maxOutputBytes, 1<<10
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if message := format.OneLine(stderr.buffer.String()); message != "" {
			return nil, fmt.Errorf("%w: %s", err, message)
		}
		return nil, err
	}
	return stdout.buffer.Bytes(), nil
}

// boundedBuffer keeps a runaway command from filling memory. Writes past the
// limit are reported as accepted and discarded, so the child is not killed by a
// short-write error partway through producing output Heikou would not use.
type boundedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if remaining := b.limit - b.buffer.Len(); remaining > 0 {
		if len(data) <= remaining {
			return b.buffer.Write(data)
		}
		if _, err := b.buffer.Write(data[:remaining]); err != nil {
			return 0, err
		}
	}
	return len(data), nil
}
