package supervisor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zamborg/heikou/internal/env"
	"github.com/zamborg/heikou/internal/heikou"
)

// requireTmux returns the tmux binary. Without tmux these tests cannot run at
// all, so they skip — except where the environment insists tmux be present,
// which is how CI refuses to report a green run over an integration suite that
// silently never executed.
func requireTmux(t *testing.T) string {
	t.Helper()
	binary, err := exec.LookPath("tmux")
	if err == nil {
		return binary
	}
	if os.Getenv("HEIKOU_TEST_REQUIRE_TMUX") != "" {
		t.Fatalf("HEIKOU_TEST_REQUIRE_TMUX is set but tmux was not found on PATH: %v", err)
	}
	t.Skip("tmux is not installed")
	return ""
}

func TestTmuxLifecycleAndLiteralMessageDelivery(t *testing.T) {
	tmuxBinary := requireTmux(t)
	versionOutput, err := exec.Command(tmuxBinary, "-V").Output()
	if err != nil {
		t.Fatal(err)
	}
	tmuxVersion := strings.TrimSpace(string(versionOutput))
	legacyDeadMetadata := tmuxMayOmitDeadMetadata(tmuxVersion)
	token, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	socket := fmt.Sprintf("heikou-test-%d-%s", os.Getpid(), token)

	fixtureDir := filepath.Join(t.TempDir(), "fixture space 日本")
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(fixtureDir, "fake agent")
	script := "#!/bin/sh\n" +
		"printf 'fake ready 日本\\n'\n" +
		"IFS= read -r first\n" +
		"IFS= read -r second\n" +
		"printf 'received first <%s>\\n' \"$first\"\n" +
		"printf 'received second <%s>\\n' \"$second\"\n" +
		"exit 7\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	manager := &Tmux{binary: tmuxBinary, socket: socket, executable: wrapper}
	t.Cleanup(func() { cleanupTestTmux(manager) })
	t.Setenv(env.CodexBinary, "/bin/sh")

	marker := filepath.Join(t.TempDir(), "prompt-was-executed")
	prompt := "review this literally $(touch " + marker + ") with `backticks`"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	callerID := "018f0000-0000-4000-8000-000000000001"
	session, err := manager.Start(ctx, heikou.StartRequest{
		ID:      callerID,
		Backend: heikou.BackendCodex,
		Prompt:  prompt,
		Root:    fixtureDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != callerID || session.PaneID == "" {
		t.Fatalf("incomplete session identity: %#v", session)
	}
	storedID, err := manager.run(ctx, nil, "show-options", "-v", "-t", session.Name, "@heikou_id")
	if err != nil || strings.TrimSpace(string(storedID)) != callerID {
		t.Fatalf("caller identity was not installed at creation: value=%q err=%v", storedID, err)
	}
	canonical, err := manager.run(ctx, nil, "show-options", "-pv", "-t", session.PaneID, "@heikou_canonical")
	if err != nil || strings.TrimSpace(string(canonical)) != "1" {
		t.Fatalf("canonical marker was not installed at creation: value=%q err=%v", canonical, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("prompt was evaluated by a shell; marker stat error = %v", err)
	}

	waitFor(t, 5*time.Second, func() bool {
		preview, captureErr := manager.Capture(context.Background(), session, 20)
		return captureErr == nil && strings.Contains(preview, "fake ready 日本")
	})

	messageMarker := filepath.Join(t.TempDir(), "message-was-executed")
	message := "literal $HOME $(touch " + messageMarker + ") `ticks` 'quotes'\nsecond\tline 日本語"
	if err := manager.Send(ctx, session, message); err != nil {
		t.Fatal(err)
	}
	var observed []heikou.Session
	var observedErr error
	var deadObservedAt time.Time
	deadline := time.Now().Add(5 * time.Second)
pollExit:
	for time.Now().Before(deadline) {
		observed, observedErr = manager.Sessions(context.Background())
		if observedErr == nil && len(observed) == 1 {
			if observed[0].Status == heikou.StatusFailed && !observed[0].EndedAt.IsZero() {
				break pollExit
			}
			if !observed[0].Alive() && legacyDeadMetadata {
				if deadObservedAt.IsZero() {
					deadObservedAt = time.Now()
				} else if time.Since(deadObservedAt) >= 500*time.Millisecond {
					break pollExit
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if observedErr != nil || len(observed) != 1 || observed[0].Alive() {
		preview, captureErr := manager.Capture(context.Background(), session, 20)
		t.Fatalf("multiline delivery did not finish: sessions=%#v listErr=%v capture=%q captureErr=%v", observed, observedErr, preview, captureErr)
	}
	if _, err := os.Stat(messageMarker); !os.IsNotExist(err) {
		t.Fatalf("message was evaluated by a shell; marker stat error = %v", err)
	}

	sessions, err := manager.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("Sessions() returned %d sessions, want 1", len(sessions))
	}
	finished := sessions[0]
	rawExitMetadata, err := manager.run(ctx, nil, "display-message", "-p", "-t", finished.PaneID, "#{pane_dead_status}|#{pane_dead_signal}|#{pane_dead_time}")
	if err != nil {
		t.Fatal(err)
	}
	metadata := strings.TrimSpace(string(rawExitMetadata))
	fields := strings.Split(metadata, "|")
	switch {
	case len(fields) == 3 && fields[0] == "7":
		if finished.Status != heikou.StatusFailed || finished.ExitCode == nil || *finished.ExitCode != 7 || finished.EndedAt.IsZero() {
			t.Fatalf("exit metadata = %#v", finished)
		}
	case metadata == "||" && legacyDeadMetadata:
		// tmux before 3.5 intermittently retains a dead pane without exit fields.
		// The pane is known dead, but its exit code and end time are not known.
		if finished.Status != heikou.StatusExited || finished.ExitCode != nil || !finished.EndedAt.IsZero() {
			t.Fatalf("omitted exit metadata was projected as known: %#v", finished)
		}
		t.Logf("%s omitted retained-pane exit metadata", tmuxVersion)
	default:
		t.Fatalf("raw exit metadata = %q on %s; session = %#v", metadata, tmuxVersion, finished)
	}
	if finished.LastUserMessage != userMessagePreview(message) {
		t.Fatalf("latest user message = %q, want %q", finished.LastUserMessage, userMessagePreview(message))
	}
	preview, err := manager.Capture(ctx, finished, 40)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview, "received first <literal") || !strings.Contains(preview, "received second <second") || !strings.Contains(preview, "日本語") {
		t.Fatalf("capture did not retain literal message:\n%s", preview)
	}

	if err := manager.Stop(ctx, finished); err != nil {
		t.Fatal(err)
	}
	after, err := manager.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("Sessions() after Stop = %d, want 0", len(after))
	}
}

func tmuxMayOmitDeadMetadata(version string) bool {
	var major, minor int
	if _, err := fmt.Sscanf(strings.TrimSpace(version), "tmux %d.%d", &major, &minor); err != nil {
		return false
	}
	return major < 3 || major == 3 && minor < 5
}

func TestTmuxMayOmitDeadMetadata(t *testing.T) {
	for version, want := range map[string]bool{
		"tmux 3.3a": true,
		"tmux 3.4":  true,
		"tmux 3.5a": false,
		"tmux 3.6a": false,
		"unknown":   false,
	} {
		if got := tmuxMayOmitDeadMetadata(version); got != want {
			t.Errorf("tmuxMayOmitDeadMetadata(%q) = %v, want %v", version, got, want)
		}
	}
}

func TestRuntimeExistsFindsPaneWithMalformedProjectionMetadata(t *testing.T) {
	tmuxBinary := requireTmux(t)
	token, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	manager := &Tmux{
		binary: tmuxBinary, socket: fmt.Sprintf("heikou-test-%d-%s", os.Getpid(), token), executable: "/bin/true",
	}
	t.Cleanup(func() { cleanupTestTmux(manager) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id := "018f0000-0000-4000-8000-000000000002"
	session, err := manager.Start(ctx, heikou.StartRequest{
		ID: id, Backend: heikou.BackendNoAgent, Prompt: "partial metadata", Root: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.run(ctx, nil, "set-option", "-t", session.Name, "@heikou_prompt", "%%%not-base64%%%"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.run(ctx, nil, "set-option", "-u", "-t", session.Name, "@heikou_id"); err != nil {
		t.Fatal(err)
	}
	sessions, err := manager.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("malformed pane unexpectedly remained projectable: %#v", sessions)
	}
	exists, err := manager.RuntimeExists(ctx, id, session.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("RuntimeExists lost a pane whose rich metadata could not be parsed")
	}
}

func TestBootstrapRemovesCredentialUnsetAfterServerStart(t *testing.T) {
	tmuxBinary := requireTmux(t)
	token, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	socket := fmt.Sprintf("heikou-env-test-%d-%s", os.Getpid(), token)
	fixtureDir := t.TempDir()
	wrapper := filepath.Join(fixtureDir, "environment agent")
	script := "#!/bin/sh\n" +
		"printf 'credential=<%s>\\n' \"${HEIKOU_STALE_CREDENTIAL-unset}\"\n" +
		"exit 0\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	manager := &Tmux{binary: tmuxBinary, socket: socket, executable: wrapper}
	t.Cleanup(func() { cleanupTestTmux(manager) })
	t.Setenv(env.CodexBinary, "/bin/sh")
	t.Setenv("HEIKOU_STALE_CREDENTIAL", "old-secret")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := manager.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("HEIKOU_STALE_CREDENTIAL"); err != nil {
		t.Fatal(err)
	}

	session, err := manager.Start(ctx, heikou.StartRequest{
		ID:      "018f0000-0000-4000-8000-000000000002",
		Backend: heikou.BackendCodex,
		Prompt:  "inspect environment",
		Root:    fixtureDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		preview, captureErr := manager.Capture(context.Background(), session, 20)
		return captureErr == nil && strings.Contains(preview, "credential=<unset>")
	})
	preview, err := manager.Capture(ctx, session, 20)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(preview, "old-secret") {
		t.Fatalf("stale credential leaked into a future session:\n%s", preview)
	}
}

func TestNoAgentStartsDefaultShellWithoutInjectingLabel(t *testing.T) {
	tmuxBinary := requireTmux(t)
	token, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	socket := fmt.Sprintf("heikou-shell-test-%d-%s", os.Getpid(), token)
	root := filepath.Join(t.TempDir(), "shell root 日本")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	manager := &Tmux{binary: tmuxBinary, socket: socket, executable: "/path/that/must/not/run"}
	t.Cleanup(func() { cleanupTestTmux(manager) })
	t.Setenv("SHELL", "/bin/sh")

	marker := filepath.Join(t.TempDir(), "label-was-executed")
	label := "scratch shell $(touch " + marker + ")"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := manager.Start(ctx, heikou.StartRequest{
		ID:      "018f0000-0000-4000-8000-000000000003",
		Backend: heikou.BackendNoAgent,
		Prompt:  label,
		Root:    root,
		Command: []string{"/path/that/must/also/not/run", "--flag"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !session.Alive() {
		t.Fatalf("no-agent session is not live: %#v", session)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("no-agent label was injected into the shell: %v", err)
	}

	if err := manager.Send(ctx, session, `printf 'HEIKOU_ROOT=<%s>\n' "$PWD"`); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		preview, captureErr := manager.Capture(context.Background(), session, 30)
		return captureErr == nil && strings.Contains(preview, "HEIKOU_ROOT=<"+root+">")
	})
	preview, err := manager.Capture(ctx, session, 30)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(preview, label) {
		t.Fatalf("no-agent label appeared in shell input/output:\n%s", preview)
	}
	if err := manager.Send(ctx, session, "exit"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		current, findErr := manager.Find(context.Background(), session.ID)
		return findErr == nil && !current.Alive()
	})
}

// TestShiftEnterReachesAPaneThatNeverNegotiatedForIt pins the bootstrap
// decision that keeps Codex usable. Codex asks tmux for the kitty keyboard
// protocol, which tmux does not speak, and a pane left in the legacy key mode
// receives a bare CR for Shift-Enter -- so the key that should open a line
// submits the message instead. The reader here negotiates for nothing at all,
// which is the case that has to work.
func TestShiftEnterReachesAPaneThatNeverNegotiatedForIt(t *testing.T) {
	tmuxBinary := requireTmux(t)
	token, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	socket := fmt.Sprintf("heikou-keys-test-%d-%s", os.Getpid(), token)
	root := t.TempDir()
	keystrokes := filepath.Join(root, "keystrokes")

	manager := &Tmux{binary: tmuxBinary, socket: socket, executable: "/path/that/must/not/run"}
	t.Cleanup(func() { cleanupTestTmux(manager) })
	t.Setenv("SHELL", "/bin/sh")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	session, err := manager.Start(ctx, heikou.StartRequest{
		ID:      "018f0000-0000-4000-8000-000000000004",
		Backend: heikou.BackendNoAgent,
		Prompt:  "read raw keys",
		Root:    root,
	})
	if err != nil {
		t.Fatal(err)
	}
	// raw keeps the line discipline from rewriting a carriage return, so a
	// legacy Shift-Enter would be recorded as the CR it really is.
	if err := manager.Send(ctx, session, "stty raw -echo; cat > "+keystrokes); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		_, statErr := os.Stat(keystrokes)
		return statErr == nil
	})
	if _, err := manager.run(ctx, nil, "send-keys", "-t", session.PaneID, "S-Enter"); err != nil {
		t.Fatal(err)
	}

	// tmux old enough to reject "always" cannot encode a key for a pane that
	// never asked, so there is nothing here to assert on it. csi-u is what
	// every tmux that accepts "always" produces: the newer ones because the
	// bootstrap names it, tmux 3.4 because it is already the default there.
	if mode, modeErr := manager.run(ctx, nil, "show-options", "-sv", "extended-keys"); modeErr != nil ||
		strings.TrimSpace(string(mode)) != "always" {
		t.Skip("this tmux cannot force extended keys onto a pane that never requested them")
	}
	const want = "\x1b[13;2u"
	waitFor(t, 10*time.Second, func() bool {
		recorded, readErr := os.ReadFile(keystrokes)
		return readErr == nil && len(recorded) > 0
	})
	recorded, err := os.ReadFile(keystrokes)
	if err != nil {
		t.Fatal(err)
	}
	if string(recorded) != want {
		t.Fatalf("pane received %q for Shift-Enter, want %q; a bare %q means the runner cannot tell it from Enter",
			string(recorded), want, "\r")
	}
}

func waitFor(t *testing.T, timeout time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

func cleanupTestTmux(manager *Tmux) {
	_, _ = manager.run(context.Background(), nil, "kill-server")
	tmuxTempDir := os.Getenv("TMUX_TMPDIR")
	if tmuxTempDir == "" {
		tmuxTempDir = "/tmp"
	}
	_ = os.Remove(filepath.Join(tmuxTempDir, fmt.Sprintf("tmux-%d", os.Getuid()), manager.socket))
}
