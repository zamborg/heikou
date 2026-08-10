package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zamborg/heikou/internal/control"
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
