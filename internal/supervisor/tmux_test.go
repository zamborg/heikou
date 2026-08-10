package supervisor

import (
	"strings"
	"testing"
	"time"

	"github.com/zamborg/heikou/internal/runner"
)

func TestParseSessionTreatsPaneModeAsCount(t *testing.T) {
	fields := []string{
		"h-test", "%1", "018f0000-0000-4000-8000-000000000000", "%1",
		"", "codex", "1000", runner.Encode("task"), runner.Encode("/tmp/root"),
		"0", "", "", "1001", "/tmp/root", "codex", "0", "2", "1",
	}
	session, err := parseSession(fields)
	if err != nil {
		t.Fatal(err)
	}
	if !session.PaneInMode {
		t.Fatal("PaneInMode = false for mode count 2")
	}
	if !session.InputDisabled {
		t.Fatal("InputDisabled = false for pane_input_off 1")
	}
}

func TestCleanCaptureRemovesTmuxDeadPaneFooter(t *testing.T) {
	input := "answer\n\nPane is dead (status 7, now)\n\n"
	if got := cleanCapture(input); got != "answer" {
		t.Fatalf("cleanCapture() = %q", got)
	}
}

func TestTitleForStripsTerminalControlSequences(t *testing.T) {
	got := titleFor("safe\x1b]52;c;c2VjcmV0\x07 task\x1b[31m red\x1b[0m", "abcdef")
	if strings.Contains(got, "\x1b") || strings.Contains(got, "c2VjcmV0") {
		t.Fatalf("title retained terminal control payload: %q", got)
	}
	if got != "safe task red" {
		t.Fatalf("title = %q", got)
	}
}

func TestRuntimeMetadataParsesExitTime(t *testing.T) {
	fields := []string{
		"h-test", "%1", "018f0000-0000-4000-8000-000000000000", "%1",
		"", "claude", "1000", runner.Encode("task"), runner.Encode("/tmp/root"),
		"1", "7", "1042", "1042", "/tmp/root", "claude", "0", "0", "0",
	}
	session, err := parseSession(fields)
	if err != nil {
		t.Fatal(err)
	}
	if got := session.Runtime(time.Unix(2000, 0)); got != 42*time.Second {
		t.Fatalf("Runtime() = %s, want 42s", got)
	}
	if session.ExitCode != 7 || !strings.Contains(string(session.Status), "failed") {
		t.Fatalf("unexpected failed session: %#v", session)
	}
}

func TestValidSessionIDIsStrict(t *testing.T) {
	if !validSessionID("018f0000-0000-4000-8000-000000000000") {
		t.Fatal("valid UUID was rejected")
	}
	for _, value := range []string{"", "short", "018F0000-0000-4000-8000-000000000000", "018f0000;kill-server-4000-8000-000000000000"} {
		if validSessionID(value) {
			t.Fatalf("invalid id %q was accepted", value)
		}
	}
}

func TestEnvironmentNamesExcludeNestedTmux(t *testing.T) {
	t.Setenv("TMUX", "/tmp/socket")
	t.Setenv("HEIKOU_TEST_TOKEN", "secret")
	names := currentEnvironmentNames("STALE_TOKEN=old\n-REMOVED_TOKEN\nTERM=screen\n")
	joined := " " + strings.Join(names, " ") + " "
	if strings.Contains(joined, " TMUX ") {
		t.Fatal("update-environment includes TMUX")
	}
	if !strings.Contains(joined, " HEIKOU_TEST_TOKEN ") {
		t.Fatal("update-environment omitted a current safe variable name")
	}
	if !strings.Contains(joined, " STALE_TOKEN ") || !strings.Contains(joined, " REMOVED_TOKEN ") {
		t.Fatal("update-environment omitted names retained by the server")
	}
	if strings.Contains(joined, " TERM ") {
		t.Fatal("update-environment includes TERM")
	}
}

func TestAttachEnvironmentAllowsNestingAndRepairsDumbTerm(t *testing.T) {
	got := withoutNestedTmux([]string{
		"PATH=/bin", "TMUX=/tmp/socket", "TMUX_PANE=%1", "TERM=dumb",
	})
	joined := " " + strings.Join(got, " ") + " "
	if strings.Contains(joined, " TMUX=") || strings.Contains(joined, " TMUX_PANE=") {
		t.Fatalf("nested tmux variables retained: %#v", got)
	}
	if !strings.Contains(joined, " TERM=xterm-256color ") {
		t.Fatalf("TERM fallback missing: %#v", got)
	}
}
