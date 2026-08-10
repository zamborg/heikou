package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zamborg/heikou/internal/heikou"
)

func TestCodexArgumentsUseOptionTerminator(t *testing.T) {
	adapter, err := AdapterFor(heikou.BackendCodex)
	if err != nil {
		t.Fatal(err)
	}
	got := adapter.Arguments("--help", "ignored", "ignored")
	want := []string{"--", "--help"}
	if !slices.Equal(got, want) {
		t.Fatalf("Arguments() = %#v, want %#v", got, want)
	}
}

func TestClaudeArgumentsCarryStableSessionIdentity(t *testing.T) {
	adapter, err := AdapterFor(heikou.BackendClaude)
	if err != nil {
		t.Fatal(err)
	}
	got := adapter.Arguments("task", "title", "018f0000-0000-4000-8000-000000000000")
	want := []string{
		"--session-id", "018f0000-0000-4000-8000-000000000000",
		"--name", "title", "--", "task",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Arguments() = %#v, want %#v", got, want)
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	want := "quotes '$()` and 日本語\nsecond line"
	got, err := decode(Encode(want))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round trip = %q, want %q", got, want)
	}
}

func TestCommandEncodeRoundTrip(t *testing.T) {
	want := []string{"/a path/agent", "--flag", "", "$(touch nope)", "日本語"}
	encoded, err := EncodeCommand(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeCommand(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
}

func TestResolveCommandPreservesConfiguredArguments(t *testing.T) {
	t.Setenv("PATH", "/bin:/usr/bin")
	got, err := ResolveCommand(heikou.BackendClaude, []string{"/bin/sh", "--flag", "$(touch nope)"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/bin/sh", "--flag", "$(touch nope)"}
	if !slices.Equal(got, want) {
		t.Fatalf("ResolveCommand() = %#v, want %#v", got, want)
	}
}

func TestFirstExecutableFindsAbsoluteCandidateWithRestrictedPath(t *testing.T) {
	directory := t.TempDir()
	candidate := filepath.Join(directory, "Codex App", "codex")
	if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	got, err := firstExecutable([]string{filepath.Join(directory, "missing"), candidate})
	if err != nil {
		t.Fatal(err)
	}
	if got != candidate {
		t.Fatalf("firstExecutable() = %q, want %q", got, candidate)
	}
}

func TestResolveCommandMissingRunnerNamesBackend(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := ResolveCommand(heikou.BackendClaude, []string{"definitely-not-a-runner"})
	if err == nil || !strings.Contains(err.Error(), "claude") || !strings.Contains(err.Error(), "definitely-not-a-runner") {
		t.Fatalf("ResolveCommand() error = %v", err)
	}
}

func TestExecEncodedCarriesConfiguredArgvWithoutShellEvaluation(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "fake agent with spaces")
	result := filepath.Join(directory, "arguments.txt")
	marker := filepath.Join(directory, "shell-evaluated")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"$HEIKOU_TEST_ARGUMENTS\"\n"
	if err := os.WriteFile(target, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	configured := []string{target, "--configured", "$(touch " + marker + ")"}
	encodedCommand, err := EncodeCommand(configured)
	if err != nil {
		t.Fatal(err)
	}
	prompt := "--leading prompt with `ticks` and 日本語"
	command := exec.Command(os.Args[0], "-test.run=^TestExecEncodedHelper$")
	command.Env = append(os.Environ(),
		"HEIKOU_EXEC_HELPER=1",
		"HEIKOU_TEST_ARGUMENTS="+result,
		"HEIKOU_TEST_BACKEND=claude",
		"HEIKOU_TEST_PROMPT="+Encode(prompt),
		"HEIKOU_TEST_TITLE="+Encode("configured title"),
		"HEIKOU_TEST_COMMAND="+encodedCommand,
		"HEIKOU_SESSION_ID=018f0000-0000-4000-8000-000000000000",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v\n%s", err, output)
	}
	data, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	want := []string{
		"--configured", "$(touch " + marker + ")",
		"--session-id", "018f0000-0000-4000-8000-000000000000",
		"--name", "configured title", "--", prompt,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("configured argument was evaluated by a shell: %v", err)
	}
}

func TestExecEncodedHelper(t *testing.T) {
	if os.Getenv("HEIKOU_EXEC_HELPER") != "1" {
		return
	}
	err := ExecEncoded(
		os.Getenv("HEIKOU_TEST_BACKEND"),
		os.Getenv("HEIKOU_TEST_PROMPT"),
		os.Getenv("HEIKOU_TEST_TITLE"),
		os.Getenv("HEIKOU_TEST_COMMAND"),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(127)
	}
}
