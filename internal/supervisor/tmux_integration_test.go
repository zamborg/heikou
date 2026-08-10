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

	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/runner"
)

func TestTmuxLifecycleAndLiteralMessageDelivery(t *testing.T) {
	tmuxBinary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
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
		"IFS= read -r line\n" +
		"printf 'received <%s>\\n' \"$line\"\n" +
		"exit 7\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	manager := &Tmux{binary: tmuxBinary, socket: socket, executable: wrapper}
	t.Cleanup(func() { cleanupTestTmux(manager) })
	t.Setenv(runner.CodexBinaryEnv, "/bin/sh")

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
	message := "literal $HOME $(touch " + messageMarker + ") `ticks` 'quotes' 日本語"
	if err := manager.Send(ctx, session, message); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		sessions, listErr := manager.Sessions(context.Background())
		return listErr == nil && len(sessions) == 1 && sessions[0].Status == heikou.StatusFailed
	})
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
	if finished.ExitCode != 7 || finished.EndedAt.IsZero() {
		t.Fatalf("exit metadata = %#v", finished)
	}
	if finished.LastUserMessage != userMessagePreview(message) {
		t.Fatalf("latest user message = %q, want %q", finished.LastUserMessage, userMessagePreview(message))
	}
	preview, err := manager.Capture(ctx, finished, 40)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview, "received <") || !strings.Contains(preview, "日本語") {
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

func TestRuntimeExistsFindsPaneWithMalformedProjectionMetadata(t *testing.T) {
	tmuxBinary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
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
	tmuxBinary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
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
	t.Setenv(runner.CodexBinaryEnv, "/bin/sh")
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
	tmuxBinary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
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
