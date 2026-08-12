package main

// End-to-end tests that drive the real `h` binary as a subprocess.
//
// Every command handler in this package writes to os.Stdout and resolves its
// own controller from the environment, so none of them can be called directly
// from a test without faking the process. Driving the built binary instead
// exercises what actually ships: argument dispatch, flag parsing, the exact
// text of a refusal, the shape of --json, and the exit code. That last set is
// the contract two audiences depend on — a person at a shell, and the pilot
// agent following skills/manage-heikou.
//
// Each test gets its own Heikou home and its own tmux socket, and HOME is
// redirected too, so a bug in home resolution shows up as a failing test rather
// than as damage to the developer's real installation.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	code := m.Run()
	if directory := builtBinaryDir; directory != "" {
		os.RemoveAll(directory)
	}
	os.Exit(code)
}

var (
	buildOnce      sync.Once
	builtBinary    string
	builtBinaryDir string
	buildErr       error
	socketSequence atomic.Uint64
)

// heikouBinary builds the command under test once per run, and only when an
// end-to-end test actually asks for it, so the unit tests in this package do
// not pay for a link.
func heikouBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		builtBinaryDir, buildErr = os.MkdirTemp("", "heikou-e2e")
		if buildErr != nil {
			return
		}
		path := filepath.Join(builtBinaryDir, "h")
		output, err := exec.Command("go", "build", "-o", path, ".").CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("build h: %w\n%s", err, output)
			return
		}
		builtBinary = path
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return builtBinary
}

// requireTmux skips when tmux is absent, unless the environment insists it be
// present. CI sets HEIKOU_TEST_REQUIRE_TMUX so that a runner which lost its
// tmux install fails loudly instead of reporting a green run over a suite that
// quietly skipped itself.
func requireTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err == nil {
		return
	}
	if os.Getenv("HEIKOU_TEST_REQUIRE_TMUX") != "" {
		t.Fatal("HEIKOU_TEST_REQUIRE_TMUX is set but tmux was not found on PATH")
	}
	t.Skip("tmux is not installed")
}

type cli struct {
	t        *testing.T
	binary   string
	home     string
	userHome string
	project  string
	socket   string
}

type result struct {
	stdout string
	stderr string
	code   int
}

func newCLI(t *testing.T) *cli {
	t.Helper()
	requireTmux(t)
	binary := heikouBinary(t)

	base := t.TempDir()
	project := filepath.Join(base, "project")
	// A separate HOME, so a bug in home resolution lands here rather than in
	// the developer's real installation, and so the shell a no-agent session
	// runs has somewhere harmless to write its history.
	userHome := filepath.Join(base, "user")
	for _, directory := range []string{project, userHome} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Short and unique. A tmux socket lives at a real path under the per-user
	// socket directory, and that path is subject to the platform's sockaddr
	// length limit, so deriving the name from t.Name() risks a truncation
	// collision on long subtest names.
	socket := fmt.Sprintf("heikou-e2e-%d-%d", os.Getpid(), socketSequence.Add(1))

	harness := &cli{
		t:        t,
		binary:   binary,
		home:     filepath.Join(base, "home"),
		userHome: userHome,
		project:  project,
		socket:   socket,
	}
	t.Cleanup(harness.shutdown)
	return harness
}

// shutdown kills the private tmux server and waits for it to go. Heikou sets
// exit-empty off during bootstrap so the server outlives its last session on
// purpose; without this the suite would leak one server per test.
//
// The wait matters as much as the kill. A dying shell writes into HOME on its
// way out, and HOME points inside the directory t.TempDir is about to remove,
// so returning early makes cleanup race a file that has not been written yet.
func (c *cli) shutdown() {
	exec.Command("tmux", "-L", c.socket, "kill-server").Run()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := exec.Command("tmux", "-L", c.socket, "has-session").Run(); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	c.t.Logf("tmux socket %s did not shut down within the deadline", c.socket)
}

func (c *cli) run(args ...string) result {
	c.t.Helper()
	outcome, err := c.execute(args...)
	if err != nil {
		c.t.Fatalf("h %s: %v", strings.Join(args, " "), err)
	}
	return outcome
}

// execute runs the binary without touching *testing.T, so it is safe to call
// from a goroutine. It returns an error only when the process could not run at
// all; a nonzero exit is a result, not an error.
func (c *cli) execute(args ...string) (result, error) {
	command := exec.Command(c.binary, args...)
	command.Dir = c.project
	command.Env = append(os.Environ(),
		"HOME="+c.userHome,
		"HEIKOU_HOME="+c.home,
		"HEIKOU_TMUX_SOCKET="+c.socket,
		// Blanked so a developer's own overrides cannot reach into a test.
		"HEIKOU_CONFIG=",
		"HEIKOU_STATE=",
		"HEIKOU_DATA=",
		"HEIKOU_DEFAULT_RUNNER=",
		"HEIKOU_CODEX_BIN=",
		"HEIKOU_CLAUDE_BIN=",
		"XDG_CONFIG_HOME=",
		"XDG_STATE_HOME=",
		"XDG_DATA_HOME=",
	)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	outcome := result{}
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return result{}, err
		}
		outcome.code = exit.ExitCode()
	}
	outcome.stdout = stdout.String()
	outcome.stderr = stderr.String()
	return outcome, nil
}

// mustRun fails the test when the command did not succeed, reporting both
// streams. A refusal that a test did not expect is almost always the real
// finding, so it must not be swallowed.
func (c *cli) mustRun(args ...string) result {
	c.t.Helper()
	outcome := c.run(args...)
	if outcome.code != 0 {
		c.t.Fatalf("h %s: exit %d\nstdout: %s\nstderr: %s",
			strings.Join(args, " "), outcome.code, outcome.stdout, outcome.stderr)
	}
	return outcome
}

// mustFail asserts a nonzero exit and returns the combined output, because a
// refusal is a product feature here: the pilot is told to relay these messages
// verbatim rather than paraphrase them.
func (c *cli) mustFail(args ...string) string {
	c.t.Helper()
	outcome := c.run(args...)
	if outcome.code == 0 {
		c.t.Fatalf("h %s: expected a nonzero exit, got 0\nstdout: %s",
			strings.Join(args, " "), outcome.stdout)
	}
	return outcome.stdout + outcome.stderr
}

type snapshotJSON struct {
	Revision    uint64 `json:"revision"`
	Workstreams []struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		ArtifactDir string   `json:"artifact_dir"`
		Roots       []string `json:"roots"`
		Revision    uint64   `json:"revision"`
	} `json:"workstreams"`
	Sessions []struct {
		ID             string `json:"id"`
		Runner         string `json:"runner"`
		State          string `json:"state"`
		Title          string `json:"title"`
		DisplayTitle   string `json:"display_title"`
		InitialPrompt  string `json:"initial_prompt"`
		WorkstreamID   string `json:"workstream_id"`
		Workstream     string `json:"workstream"`
		Root           string `json:"root"`
		Available      bool   `json:"available"`
		Alive          bool   `json:"alive"`
		Orphaned       bool   `json:"orphaned"`
		ExitCode       *int   `json:"exit_code"`
		RuntimeSeconds int64  `json:"runtime_seconds"`
	} `json:"sessions"`
}

func (c *cli) snapshot() snapshotJSON {
	c.t.Helper()
	output := c.mustRun("list", "--json").stdout
	var snapshot snapshotJSON
	if err := json.Unmarshal([]byte(output), &snapshot); err != nil {
		c.t.Fatalf("decode h list --json: %v\noutput: %s", err, output)
	}
	// Every state the CLI reports must be one the pilot has been taught, since
	// AGENTS.md forbids it from describing a session in words of its own.
	for _, session := range snapshot.Sessions {
		if !documentedStates[session.State] {
			c.t.Fatalf("session %s reported state %q, which skills/manage-heikou does not document",
				session.ID, session.State)
		}
	}
	return snapshot
}

// onlySession returns the single session, insisting there is exactly one so a
// test never silently asserts against the wrong record.
func (c *cli) onlySession() snapshotJSON {
	c.t.Helper()
	snapshot := c.snapshot()
	if len(snapshot.Sessions) != 1 {
		c.t.Fatalf("want exactly 1 session, got %d", len(snapshot.Sessions))
	}
	return snapshot
}

// waitForState polls until a session reaches one of the wanted states. tmux
// reports a pane's death asynchronously, so asserting immediately after h stop
// would be a race.
func (c *cli) waitForState(sessionID string, wanted ...string) string {
	c.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	last := ""
	for time.Now().Before(deadline) {
		for _, session := range c.snapshot().Sessions {
			if session.ID != sessionID {
				continue
			}
			last = session.State
			for _, want := range wanted {
				if session.State == want {
					return session.State
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.t.Fatalf("session %s never reached %v; last state %q", sessionID, wanted, last)
	return ""
}

func mustContain(t *testing.T, got, want, context string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%s: output %q does not contain %q", context, got, want)
	}
}

// TestCLIOrganizesAWorkstreamEndToEnd walks the five operations the pilot is
// documented to perform, in one continuous flow against one installation.
func TestCLIOrganizesAWorkstreamEndToEnd(t *testing.T) {
	harness := newCLI(t)

	// 1. Make a new workstream. The flags deliberately follow the positional
	// name, which is how a person and an agent both tend to write it.
	created := harness.mustRun("ws", "create", "API work", "-C", harness.project, "-d", "the api")
	mustContain(t, created.stdout, "created workstream API work", "ws create")

	snapshot := harness.snapshot()
	if len(snapshot.Workstreams) != 1 {
		t.Fatalf("want 1 workstream, got %d", len(snapshot.Workstreams))
	}
	workstream := snapshot.Workstreams[0]
	if workstream.Name != "API work" {
		t.Fatalf("workstream name = %q, want %q", workstream.Name, "API work")
	}
	if workstream.Description != "the api" {
		t.Fatalf("workstream description = %q, want %q", workstream.Description, "the api")
	}
	if len(workstream.Roots) != 1 || workstream.Roots[0] != harness.project {
		t.Fatalf("workstream roots = %v, want [%s]", workstream.Roots, harness.project)
	}

	// 2. Add a directory to the workstream.
	second := filepath.Join(t.TempDir(), "api-client")
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	harness.mustRun("ws", "root", "add", "API", second)
	if roots := harness.snapshot().Workstreams[0].Roots; len(roots) != 2 {
		t.Fatalf("want 2 roots after add, got %v", roots)
	}

	// 3. Start a session in the workstream. no-agent runs a plain shell, so the
	// test needs no coding agent installed.
	started := harness.mustRun("spawn", "-r", "no-agent", "-C", second, "-w", "API", "scratch shell")
	mustContain(t, started.stdout, "started no-agent", "spawn")

	session := harness.onlySession().Sessions[0]
	if session.Workstream != "API work" {
		t.Fatalf("session workstream = %q, want %q", session.Workstream, "API work")
	}
	if session.Root != second {
		t.Fatalf("session root = %q, want %q", session.Root, second)
	}
	if session.InitialPrompt != "scratch shell" {
		t.Fatalf("session initial_prompt = %q, want %q", session.InitialPrompt, "scratch shell")
	}
	harness.waitForState(session.ID, "live")

	// 4. Give the session a durable name. The title leads with a dash and
	// follows an explicit --, which is the documented way to pass a positional
	// that would otherwise look like a flag.
	harness.mustRun("title", session.ID, "--", "-retry bug")
	titled := harness.onlySession().Sessions[0]
	if titled.Title != "-retry bug" {
		t.Fatalf("session title = %q, want %q", titled.Title, "-retry bug")
	}
	if titled.DisplayTitle != "-retry bug" {
		t.Fatalf("session display_title = %q, want the durable title", titled.DisplayTitle)
	}

	// A cleared title falls back to the initial prompt rather than to nothing.
	harness.mustRun("title", session.ID, "--clear")
	cleared := harness.onlySession().Sessions[0]
	if cleared.Title != "" {
		t.Fatalf("session title = %q after --clear, want empty", cleared.Title)
	}
	if cleared.DisplayTitle != "scratch shell" {
		t.Fatalf("session display_title = %q after --clear, want the initial prompt", cleared.DisplayTitle)
	}

	// 5. Notes live in the workstream's artifact directory, and the directory
	// is durable across a rename — renaming must not strand a note.
	notes := filepath.Join(workstream.ArtifactDir, "notes.md")
	if err := os.WriteFile(notes, []byte("# decisions\n"), 0o600); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	harness.mustRun("ws", "rename", "API", "Platform API")
	renamed := harness.snapshot().Workstreams[0]
	if renamed.Name != "Platform API" {
		t.Fatalf("workstream name = %q after rename, want %q", renamed.Name, "Platform API")
	}
	if renamed.ArtifactDir != workstream.ArtifactDir {
		t.Fatalf("artifact_dir moved on rename: %q -> %q", workstream.ArtifactDir, renamed.ArtifactDir)
	}
	if contents, err := os.ReadFile(notes); err != nil || string(contents) != "# decisions\n" {
		t.Fatalf("notes did not survive the rename: %q, %v", contents, err)
	}

	// Moving a session out and back keeps the record intact.
	harness.mustRun("move", session.ID, "--ungrouped")
	if got := harness.onlySession().Sessions[0].Workstream; got != "" && got != "Ungrouped" {
		t.Fatalf("session workstream = %q after --ungrouped", got)
	}
	harness.mustRun("move", session.ID, "--workstream", "Platform")
	if got := harness.onlySession().Sessions[0].Workstream; got != "Platform API" {
		t.Fatalf("session workstream = %q after move back, want %q", got, "Platform API")
	}

	// Stopping then deleting retires the record. Deleting a live session is
	// refused, so the order here is itself part of the contract.
	mustContain(t, harness.mustFail("delete", session.ID, "--yes"),
		"stop it first", "delete before stop")
	harness.mustRun("stop", session.ID)
	harness.waitForState(session.ID, "stopped", "exited")
	harness.mustRun("delete", session.ID, "--yes")
	if sessions := harness.snapshot().Sessions; len(sessions) != 0 {
		t.Fatalf("want 0 sessions after delete, got %d", len(sessions))
	}
}

// TestCLIPeekReportsCurrentFrameOnly pins the flag the pilot relies on to avoid
// presenting a pane capture as session history.
func TestCLIPeekReportsCurrentFrameOnly(t *testing.T) {
	harness := newCLI(t)
	harness.mustRun("ws", "create", "peeking", "-C", harness.project)
	harness.mustRun("spawn", "-r", "no-agent", "-C", harness.project, "-w", "peeking", "a shell")
	session := harness.onlySession().Sessions[0]
	harness.waitForState(session.ID, "live")

	var peeked struct {
		SessionID string `json:"session_id"`
		State     string `json:"state"`
		Capture   string `json:"capture"`
		// A pointer so an omitted key is distinguishable from a false one.
		FrameOnly *bool `json:"capture_is_current_frame_only"`
	}
	output := harness.mustRun("peek", session.ID, "--json").stdout
	if err := json.Unmarshal([]byte(output), &peeked); err != nil {
		t.Fatalf("decode h peek --json: %v\noutput: %s", err, output)
	}
	if peeked.SessionID != session.ID {
		t.Fatalf("peek session_id = %q, want %q", peeked.SessionID, session.ID)
	}
	if peeked.FrameOnly == nil {
		t.Fatal("peek --json omitted capture_is_current_frame_only; the pilot reads this to avoid calling a capture a transcript")
	}
	if !*peeked.FrameOnly {
		t.Fatal("peek --json reported capture_is_current_frame_only=false, but a capture is never a transcript")
	}
}

// TestCLIRefusalsExitNonzeroWithTheDocumentedReason covers the guardrails
// skills/manage-heikou promises. Each of these is a refusal a person or an
// agent will hit, and each message names the way forward.
func TestCLIRefusalsExitNonzeroWithTheDocumentedReason(t *testing.T) {
	harness := newCLI(t)
	harness.mustRun("ws", "create", "api", "-C", harness.project, "-d", "the api")

	elsewhere := t.TempDir()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "unknown verb",
			args: []string{"bogus-verb"},
			want: `unknown command "bogus-verb"`,
		},
		{
			name: "duplicate workstream name",
			args: []string{"ws", "create", "api", "-C", harness.project},
			want: `an active workstream named "api" already exists`,
		},
		{
			name: "launch into an unregistered root",
			args: []string{"spawn", "-r", "no-agent", "-C", elsewhere, "-w", "api", "wrong root"},
			want: "is not registered in workstream",
		},
		{
			name: "remove the only root",
			args: []string{"ws", "root", "rm", "api", harness.project},
			want: "must keep at least one root",
		},
		{
			name: "archive without consent",
			args: []string{"ws", "archive", "api"},
			want: "pass --yes to confirm",
		},
		{
			name: "delete without consent",
			args: []string{"delete", "deadbeef"},
			want: "pass --yes to confirm",
		},
		{
			name: "unknown session",
			args: []string{"move", "no-such-session", "--workstream", "api"},
			want: `no session matches "no-such-session"`,
		},
		{
			name: "unknown workstream",
			args: []string{"ws", "rename", "no-such-workstream", "whatever"},
			want: `no active workstream matches "no-such-workstream"`,
		},
		{
			name: "unknown flag",
			args: []string{"ws", "create", "beta", "-C", harness.project, "--nonsense"},
			want: "nonsense",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mustContain(t, harness.mustFail(test.args...), test.want, "h "+strings.Join(test.args, " "))
		})
	}
}

// TestCLIAcceptsFlagsAfterPositionals is a regression guard. Go's flag package
// stops parsing at the first positional, so before parseAnywhere existed
// `h ws create "API work" -C DIR -d TEXT` created a workstream literally named
// `API work -C DIR -d TEXT` rooted at the current directory. It was a silent
// wrong result, which is the worst kind, and it reached a release.
func TestCLIAcceptsFlagsAfterPositionals(t *testing.T) {
	harness := newCLI(t)

	harness.mustRun("ws", "create", "Backend", "-C", harness.project, "-d", "flags after the name")
	workstream := harness.snapshot().Workstreams[0]
	if workstream.Name != "Backend" {
		t.Fatalf("workstream name = %q; a trailing flag was folded into the positional", workstream.Name)
	}
	if workstream.Description != "flags after the name" {
		t.Fatalf("workstream description = %q, want the -d value", workstream.Description)
	}

	// h spawn is the sharpest case: the folded flags silently changed the
	// runner, the root, and the workstream while the command still reported
	// success, and this is the one verb that launches a real agent into a real
	// repository.
	harness.mustRun("spawn", "no runner flag first", "-r", "no-agent", "-C", harness.project, "-w", "Backend")
	session := harness.onlySession().Sessions[0]
	if session.InitialPrompt != "no runner flag first" {
		t.Fatalf("session initial_prompt = %q; a trailing flag was folded into the prompt", session.InitialPrompt)
	}
	if session.Runner != "no-agent" {
		t.Fatalf("session runner = %q, want no-agent from the trailing -r", session.Runner)
	}
	if session.Workstream != "Backend" {
		t.Fatalf("session workstream = %q, want Backend from the trailing -w", session.Workstream)
	}
	harness.waitForState(session.ID, "live")

	// h send takes the message as trailing positionals, so a flag written after
	// it used to be delivered to the agent as text instead of being honoured.
	sent := harness.mustRun("send", session.ID, "hello there", "--json")
	var delivery struct {
		SessionID string `json:"session_id"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal([]byte(sent.stdout), &delivery); err != nil {
		t.Fatalf("h send did not honour a trailing --json: %v\noutput: %s", err, sent.stdout)
	}
	if delivery.Status != "sent" {
		t.Fatalf("h send status = %q, want sent", delivery.Status)
	}

	// The escape hatch has to work, because it is the only way to pass text
	// that begins with a dash.
	harness.mustRun("title", session.ID, "--", "-w not a flag")
	if got := harness.onlySession().Sessions[0].Title; got != "-w not a flag" {
		t.Fatalf("session title = %q, want the literal text after --", got)
	}

	// Without the escape, a leading-dash positional must fail loudly rather
	// than be silently accepted as text.
	mustContain(t, harness.mustFail("title", session.ID, "-w not a flag"),
		"not defined", "leading-dash positional without --")
}

// TestCLIRefusesAmbiguousPrefixesRatherThanGuessing checks the resolver picks a
// unique prefix but errors on a shared one. Guessing here would silently
// organize the wrong workstream.
func TestCLIRefusesAmbiguousPrefixesRatherThanGuessing(t *testing.T) {
	harness := newCLI(t)
	harness.mustRun("ws", "create", "Backend", "-C", harness.project)
	harness.mustRun("ws", "create", "Backlog", "-C", harness.project)

	mustContain(t, harness.mustFail("ws", "rename", "Back", "whatever"),
		"is ambiguous", "ambiguous prefix")

	// The unambiguous prefix still resolves.
	harness.mustRun("ws", "rename", "Backend", "Backend renamed")
	names := map[string]bool{}
	for _, workstream := range harness.snapshot().Workstreams {
		names[workstream.Name] = true
	}
	if !names["Backend renamed"] || !names["Backlog"] {
		t.Fatalf("unexpected workstreams after rename: %v", names)
	}
}

// TestCLIConcurrentWritersDoNotLoseUpdates exercises the advisory lock across
// processes, which is the only place it does any work. The in-process test in
// internal/workstream covers goroutines sharing one FileStore; nothing covered
// separate processes racing for the same state file.
//
// That race is now ordinary rather than exotic: the pilot runs h while the
// user has a dashboard open, so two independent writers are the normal case.
func TestCLIConcurrentWritersDoNotLoseUpdates(t *testing.T) {
	harness := newCLI(t)
	// One write first, so every racing process contends for an existing file
	// rather than for its creation.
	harness.mustRun("ws", "create", "first", "-C", harness.project)

	const writers = 8
	outcomes := make([]result, writers)
	failures := make([]error, writers)
	var waiting sync.WaitGroup
	for index := 0; index < writers; index++ {
		waiting.Add(1)
		go func(index int) {
			defer waiting.Done()
			outcomes[index], failures[index] = harness.execute(
				"ws", "create", fmt.Sprintf("stream-%02d", index), "-C", harness.project)
		}(index)
	}
	waiting.Wait()

	for index, err := range failures {
		if err != nil {
			t.Fatalf("writer %d could not run: %v", index, err)
		}
		if outcomes[index].code != 0 {
			t.Errorf("writer %d exited %d; the lock should serialise writers, not reject them\nstderr: %s",
				index, outcomes[index].code, outcomes[index].stderr)
		}
	}

	// Every name must have survived. A lost update here would mean one writer
	// read the state, another committed, and the first wrote over it.
	snapshot := harness.snapshot()
	found := map[string]bool{}
	for _, workstream := range snapshot.Workstreams {
		found[workstream.Name] = true
	}
	for index := 0; index < writers; index++ {
		name := fmt.Sprintf("stream-%02d", index)
		if !found[name] {
			t.Errorf("workstream %q was lost; %d of %d survived", name, len(snapshot.Workstreams), writers+1)
		}
	}
	if len(snapshot.Workstreams) != writers+1 {
		t.Errorf("want %d workstreams, got %d", writers+1, len(snapshot.Workstreams))
	}
	// The revision counter has to have advanced once per committed write.
	if snapshot.Revision < uint64(writers+1) {
		t.Errorf("revision = %d after %d writes; it should count every commit",
			snapshot.Revision, writers+1)
	}
}

// TestCLIWritesNothingOutsideItsHome guards the isolation the rest of this file
// depends on, and the promise h makes to a user who sets HEIKOU_HOME.
func TestCLIWritesNothingOutsideItsHome(t *testing.T) {
	harness := newCLI(t)
	surrounding := filepath.Dir(harness.home)

	harness.mustRun("ws", "create", "contained", "-C", harness.project, "-d", "state stays home")
	harness.mustRun("list", "--json")

	entries, err := os.ReadDir(surrounding)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "home", "project", "user":
		default:
			t.Fatalf("h wrote %q outside its home directory", entry.Name())
		}
	}
	// HEIKOU_HOME is set, so nothing may fall back to ~/.heikou.
	if _, err := os.Stat(filepath.Join(harness.userHome, ".heikou")); !os.IsNotExist(err) {
		t.Fatalf("h created ~/.heikou despite HEIKOU_HOME being set: %v", err)
	}

	// The pilot documents itself into the home directory on first run.
	for _, relative := range []string{"AGENTS.md", "CLAUDE.md", "skills/manage-heikou/SKILL.md", "state.json"} {
		if _, err := os.Stat(filepath.Join(harness.home, relative)); err != nil {
			t.Fatalf("expected %s in the heikou home: %v", relative, err)
		}
	}
}
