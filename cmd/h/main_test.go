package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zamborg/heikou/internal/config"
	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/workstream"
)

func TestRouteGlobalCommand(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		handled bool
		want    string
	}{
		{name: "help", args: []string{"help"}, handled: true, want: "heikou — a fast dashboard"},
		{name: "short help", args: []string{"-h"}, handled: true, want: "heikou — a fast dashboard"},
		{name: "long help", args: []string{"--help"}, handled: true, want: "heikou — a fast dashboard"},
		{name: "version", args: []string{"version"}, handled: true, want: "heikou " + version + "\n"},
		{name: "long version", args: []string{"--version"}, handled: true, want: "heikou " + version + "\n"},
		{name: "dashboard", handled: false},
		{name: "dashboard flag", args: []string{"--runner", "no-agent"}, handled: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			handled := routeGlobalCommand(test.args, &output)
			if handled != test.handled {
				t.Fatalf("routeGlobalCommand() handled = %v, want %v", handled, test.handled)
			}
			if test.want == "" {
				if output.Len() != 0 {
					t.Fatalf("routeGlobalCommand() output = %q, want empty", output.String())
				}
				return
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("routeGlobalCommand() output = %q, want substring %q", output.String(), test.want)
			}
		})
	}
}

func TestRunRoutesLongVersionBeforeDashboardFlags(t *testing.T) {
	var output bytes.Buffer
	if err := runWithGlobalOutput([]string{"--version"}, &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "heikou "+version+"\n"; got != want {
		t.Fatalf("runWithGlobalOutput() = %q, want %q", got, want)
	}
}

func TestReadmeAdvertisesCurrentVersion(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	if want := "github.com/zamborg/heikou/cmd/h@v" + version; !strings.Contains(string(readme), want) {
		t.Fatalf("README install command does not advertise %q", want)
	}
}

func TestHelpAdvertisesQuickstart(t *testing.T) {
	var output bytes.Buffer
	printHelp(&output)
	for _, want := range []string{
		"h quickstart [-r claude|codex] [-C DIR]",
		"h list [--json]",
		"Ctrl-G            resize snapshot/context",
		"Organizer Shift-↑/↓ reorder a named workstream",
		"Organizer r       rename a workstream or edit/clear a session title",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("help does not advertise %q:\n%s", want, output.String())
		}
	}
}

func TestQuickstartBackendPrefersClaudeAndRejectsNoAgent(t *testing.T) {
	settings := config.Default()
	settings.Commands[string(heikou.BackendClaude)] = []string{"/bin/sh"}
	settings.Commands[string(heikou.BackendCodex)] = []string{"/bin/sh"}
	backend, err := quickstartBackend("", settings)
	if err != nil {
		t.Fatal(err)
	}
	if backend != heikou.BackendClaude {
		t.Fatalf("quickstart backend = %q, want claude", backend)
	}
	if _, err := quickstartBackend("no-agent", settings); err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("no-agent quickstart error = %v", err)
	}
}

func TestQuickstartBackendFallsBackToCodex(t *testing.T) {
	settings := config.Default()
	settings.Commands[string(heikou.BackendClaude)] = []string{"/definitely/missing/heikou-claude"}
	settings.Commands[string(heikou.BackendCodex)] = []string{"/bin/sh"}
	backend, err := quickstartBackend("", settings)
	if err != nil {
		t.Fatal(err)
	}
	if backend != heikou.BackendCodex {
		t.Fatalf("quickstart fallback = %q, want codex", backend)
	}
	if _, err := quickstartBackend("claude", settings); err == nil {
		t.Fatal("explicit missing Claude runner did not fail")
	}
}

func TestQuickstartPromptEmbedsCanonicalSkill(t *testing.T) {
	prompt := strings.Join(strings.Fields(quickstartPrompt()), " ")
	for _, want := range []string{"name: learn-heikou", "If this guide itself is running inside a Heikou session", "already marked as the move source", "Only if that marker is absent", "Ctrl-b", "Ctrl-G", "Shift-Up"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("quickstart prompt is missing %q", want)
		}
	}
}

func TestQuickstartHelpReturnsSuccess(t *testing.T) {
	if err := runQuickstart([]string{"-h"}); err != nil {
		t.Fatalf("quickstart help: %v", err)
	}
}

func TestSupportedTmuxVersion(t *testing.T) {
	tests := []struct {
		value     string
		supported bool
		known     bool
	}{
		{"tmux 3.2a", false, true},
		{"tmux 3.3", true, true},
		{"tmux 3.6a", true, true},
		{"tmux 4.0", true, true},
		{"unknown", false, false},
	}
	for _, test := range tests {
		supported, known := supportedTmuxVersion(test.value)
		if supported != test.supported || known != test.known {
			t.Errorf("supportedTmuxVersion(%q) = (%v, %v), want (%v, %v)",
				test.value, supported, known, test.supported, test.known)
		}
	}
}

func TestResolveWorkstreamByNameAndRejectAmbiguity(t *testing.T) {
	snapshot := control.Snapshot{Workstreams: []workstream.Workstream{
		{ID: "018f0000-0000-4000-8000-000000000001", Name: "Heikou Core"},
		{ID: "018f0000-0000-4000-8000-000000000002", Name: "Heikou Docs"},
	}}
	id, err := resolveWorkstream(snapshot, "heikou core")
	if err != nil || id != snapshot.Workstreams[0].ID {
		t.Fatalf("exact name resolution = (%q, %v)", id, err)
	}
	if _, err := resolveWorkstream(snapshot, "hei"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous prefix error = %v", err)
	}
}

func TestOneLineStripsTerminalControlSequences(t *testing.T) {
	got := oneLine("safe\x1b]52;c;c2VjcmV0\x07 text\nnext\x1b[31m red\x1b[0m")
	if strings.Contains(got, "\x1b") || strings.Contains(got, "c2VjcmV0") {
		t.Fatalf("oneLine retained terminal control payload: %q", got)
	}
	if got != "safe text next red" {
		t.Fatalf("oneLine = %q", got)
	}
}

func TestMachineSnapshotIncludesDurableTitleAndHonestUnknownExit(t *testing.T) {
	id := "018f0000-0000-4000-8000-000000000081"
	workstreamID := "018f0000-0000-4000-8000-000000000082"
	runtime := heikou.Session{
		ID: id, Name: "h-" + id, Backend: heikou.BackendCodex,
		Status: heikou.StatusExited, ExitCode: nil, StartedAt: time.Now().Add(-time.Minute),
	}
	snapshot := control.Snapshot{
		Revision:    9,
		Workstreams: []workstream.Workstream{{ID: workstreamID, Name: "Release", Roots: []string{"/tmp"}}},
		Sessions: []control.Session{{
			ID: id, Backend: heikou.BackendCodex, Prompt: "initial task", LastUserMessage: "latest follow-up",
			Root: "/tmp", WorkstreamID: workstreamID, Status: control.StatusExited, Durable: true,
			Record: workstream.SessionRecord{ID: id, Title: "Ship macOS build"}, Runtime: &runtime,
		}},
	}

	machine := newCLISnapshot(snapshot)
	if len(machine.Sessions) != 1 {
		t.Fatalf("machine sessions = %#v", machine.Sessions)
	}
	session := machine.Sessions[0]
	if session.Title != "Ship macOS build" || session.DisplayTitle != session.Title || session.LatestViaHeikou != "latest follow-up" {
		t.Fatalf("machine title projection = %#v", session)
	}
	if session.ExitCode != nil || session.State != "exited" {
		t.Fatalf("unknown exit projection = stable state %q code %#v", session.State, session.ExitCode)
	}

	var output bytes.Buffer
	if err := writeJSON(&output, machine); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("machine JSON is invalid: %v\n%s", err, output.String())
	}
	if decoded["revision"] != float64(9) {
		t.Fatalf("decoded revision = %#v", decoded["revision"])
	}
	sessions, ok := decoded["sessions"].([]any)
	if !ok || len(sessions) != 1 {
		t.Fatalf("decoded sessions = %#v", decoded["sessions"])
	}
	decodedSession, ok := sessions[0].(map[string]any)
	if !ok {
		t.Fatalf("decoded session = %#v", sessions[0])
	}
	if exitCode, exists := decodedSession["exit_code"]; !exists || exitCode != nil {
		t.Fatalf("unknown exit_code = %#v (present %v), want explicit null", exitCode, exists)
	}
}

func TestMachineSnapshotKeepsStateStableWhenExitCodeIsNonzero(t *testing.T) {
	code := 7
	id := "018f0000-0000-4000-8000-000000000083"
	runtime := heikou.Session{
		ID: id, Backend: heikou.BackendCodex, Status: heikou.StatusFailed,
		ExitCode: &code, StartedAt: time.Now().Add(-time.Minute),
	}
	machine := newCLISnapshot(control.Snapshot{Sessions: []control.Session{{
		ID: id, Backend: heikou.BackendCodex, Prompt: "failing task", Root: "/tmp",
		Status: control.StatusExited, Durable: true, Runtime: &runtime,
	}}})
	if len(machine.Sessions) != 1 {
		t.Fatalf("machine sessions = %#v", machine.Sessions)
	}
	session := machine.Sessions[0]
	if session.State != "exited" || session.ExitCode == nil || *session.ExitCode != 7 {
		t.Fatalf("failed process projection = state %q code %#v", session.State, session.ExitCode)
	}
}
