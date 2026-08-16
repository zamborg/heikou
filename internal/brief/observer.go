package brief

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zamborg/heikou/internal/config"
	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/env"
	"github.com/zamborg/heikou/internal/format"
	"github.com/zamborg/heikou/internal/transcript"
)

const (
	// activityInterval is the floor between transcript reads for one session.
	// It is not configurable because it is not a cost the user chose: unlike a
	// command source, this one reads the tail of a file the runner is already
	// writing, so the price of a shorter interval is a page-cache read rather
	// than a process.
	activityInterval = 5 * time.Second
	// activityTimeout is the backstop for that read. A local file cannot hang,
	// but a transcript directory on a stalled network mount can, and a pass
	// must not be the thing that discovers it.
	activityTimeout = 5 * time.Second
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

// Observer runs the sources that have to go and find something out, on its own
// cadence, and returns what they reported. Nothing here is called from the
// render path.
//
// Two kinds of source live behind it. A configured command source runs a
// user's program; the built-in activity source reads the tail of the
// transcript the runner is already writing. They differ in what they cost and
// in nothing else: both are due on an interval, both are gated on the session
// having moved, and both cache a line that a render reads without blocking.
type Observer struct {
	sources map[SourceID]config.BriefSourceConfig
	// activity is whether the layout named the built-in activity source.
	// Nothing runs for a source no slot can show.
	activity bool
	// reader is a field so tests can point at a fixture directory. Its zero
	// value reads the real location under the user's home.
	reader transcript.Reader
	run    runFunc
	now    func() time.Time
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
	layout := LayoutFrom(settings)
	return &Observer{
		sources:  sources,
		activity: slices.Contains(layout.Lead, SourceActivity) || slices.Contains(layout.Detail, SourceActivity),
		run:      runCommand,
		now:      time.Now,
	}
}

// Configured reports whether anything would run at all, so the dashboard can
// skip scheduling entirely for a layout that asks nothing of it.
func (o *Observer) Configured() bool { return o != nil && (len(o.sources) > 0 || o.activity) }

type pendingRun struct {
	key      Key
	session  control.Session
	interval time.Duration
	timeout  time.Duration
	observe  func(context.Context, control.Session) (string, error)
}

// candidates is every source that could say something about one session.
//
// A source that does not apply is not merely skipped: its previous observation
// is dropped, because Observe rebuilds the map from what applies now. That is
// what keeps "running make check" off a row whose session has exited, which
// would be the same looks-current failure as freezing a failed source's text.
func (o *Observer) candidates(session control.Session) []pendingRun {
	runs := make([]pendingRun, 0, len(o.sources)+1)
	for id, settings := range o.sources {
		runs = append(runs, pendingRun{
			key:      Key{Session: session.ID, Source: id},
			session:  session,
			interval: time.Duration(settings.IntervalSeconds) * time.Second,
			timeout:  time.Duration(settings.TimeoutSeconds) * time.Second,
			observe:  o.commandObserver(settings),
		})
	}
	if o.activity && session.Alive() {
		runs = append(runs, pendingRun{
			key:      Key{Session: session.ID, Source: SourceActivity},
			session:  session,
			interval: activityInterval,
			timeout:  activityTimeout,
			observe:  o.observeActivity,
		})
	}
	return runs
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
//
// A source that found nothing still records an empty observation. Storing it is
// what makes the interval apply to a source that will keep finding nothing —
// a Codex session whose rollout Heikou cannot identify, say — instead of
// letting it come due on every pass and spend the per-pass cap forever.
func (o *Observer) Observe(ctx context.Context, sessions []control.Session, previous Observations) (Observations, Report) {
	next := make(Observations, len(previous))
	var report Report
	if !o.Configured() {
		return next, report
	}

	now := o.now()
	var due []pendingRun
	for _, session := range sessions {
		for _, candidate := range o.candidates(session) {
			observation, seen := previous[candidate.key]
			if seen {
				next[candidate.key] = observation
			}
			if !isDue(observation, seen, session, candidate.interval, now) {
				continue
			}
			due = append(due, candidate)
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

func isDue(observation Observation, seen bool, session control.Session, interval time.Duration, now time.Time) bool {
	if !seen {
		return true
	}
	if now.Sub(observation.ObservedAt) < interval {
		return false
	}
	return !session.LastActivity().Equal(observation.ActivityAt)
}

func (o *Observer) observeOne(ctx context.Context, run pendingRun) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, run.timeout)
	defer cancel()
	text, err := run.observe(runCtx, run.session)
	if err != nil {
		return "", fmt.Errorf("brief source %q for session %s: %w", run.key.Source, format.ShortID(run.key.Session), err)
	}
	return text, nil
}

// commandObserver runs one configured program and keeps its first line.
func (o *Observer) commandObserver(settings config.BriefSourceConfig) func(context.Context, control.Session) (string, error) {
	return func(ctx context.Context, session control.Session) (string, error) {
		output, err := o.run(ctx, settings.Command, sessionEnvironment(session))
		if err != nil {
			return "", err
		}
		if !utf8.Valid(output) {
			return "", errors.New("output is not valid UTF-8")
		}
		line, _, _ := bytes.Cut(output, []byte("\n"))
		return briefText(string(line)), nil
	}
}

// observeActivity reads the tail of the transcript the runner writes and
// phrases the last record as a line.
//
// A runner with no transcript Heikou can locate, and a session whose transcript
// has not been written yet, both produce empty text rather than an error. That
// is the ordinary case for Codex and for a session in its first second, and it
// is not a failure to report: the slot falls through to the next source.
func (o *Observer) observeActivity(ctx context.Context, session control.Session) (string, error) {
	// A file read is not interruptible, so the deadline can only be honoured
	// before it starts. That is enough for what it guards: a pass that has
	// already been abandoned should not begin more work.
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// The transcript is filed under the runner's conversation id, which is the
	// durable session id only for a session Heikou launched fresh. A resumed
	// session asked for by its own id would look for a file Claude never wrote,
	// and pay the fallback scan for it on every pass.
	item, err := o.reader.ReadActivity(transcript.Request{
		Runner: session.Backend, SessionID: session.ConversationID(), Root: session.Root,
	})
	if err != nil {
		return "", err
	}
	return briefText(activityLine(item)), nil
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
