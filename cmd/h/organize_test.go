package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Flags written after a positional argument used to be swallowed into that
// argument, so `h ws create "API work" -C ~/proj` silently produced a
// workstream literally named `API work -C ~/proj` rooted at the wrong
// directory. Both people and agents compose commands in that order.
func TestParseAnywhereAcceptsFlagsAroundPositionals(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantRoot   string
		wantDesc   string
		wantJSON   bool
		wantRemain string
	}{
		{
			name:       "flags first",
			args:       []string{"-C", "/tmp/a", "-d", "desc", "API work"},
			wantRoot:   "/tmp/a",
			wantDesc:   "desc",
			wantRemain: "API work",
		},
		{
			name:       "flags after the positional",
			args:       []string{"API work", "-C", "/tmp/a", "-d", "desc"},
			wantRoot:   "/tmp/a",
			wantDesc:   "desc",
			wantRemain: "API work",
		},
		{
			name:       "flags surrounding the positional",
			args:       []string{"-C", "/tmp/a", "API work", "-d", "desc"},
			wantRoot:   "/tmp/a",
			wantDesc:   "desc",
			wantRemain: "API work",
		},
		{
			name:       "bool flag consumes no value",
			args:       []string{"API work", "--json", "-C", "/tmp/a"},
			wantRoot:   "/tmp/a",
			wantJSON:   true,
			wantRemain: "API work",
		},
		{
			name:       "equals form",
			args:       []string{"API work", "-C=/tmp/a"},
			wantRoot:   "/tmp/a",
			wantRemain: "API work",
		},
		{
			name:       "multi-word positional stays intact",
			args:       []string{"-C", "/tmp/a", "API", "work", "here"},
			wantRoot:   "/tmp/a",
			wantRemain: "API work here",
		},
		{
			name:       "double dash stops flag scanning",
			args:       []string{"-C", "/tmp/a", "--", "-not-a-flag"},
			wantRoot:   "/tmp/a",
			wantRemain: "-not-a-flag",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			flags := (&app{err: io.Discard}).newFlagSet("test")
			root := flags.String("root", "", "root")
			flags.StringVar(root, "C", *root, "root")
			description := flags.String("description", "", "description")
			flags.StringVar(description, "d", *description, "description")
			jsonOutput := flags.Bool("json", false, "json")

			if err := parseAnywhere(flags, testCase.args); err != nil {
				t.Fatalf("parseAnywhere: %v", err)
			}
			if *root != testCase.wantRoot {
				t.Errorf("root = %q, want %q", *root, testCase.wantRoot)
			}
			if *description != testCase.wantDesc {
				t.Errorf("description = %q, want %q", *description, testCase.wantDesc)
			}
			if *jsonOutput != testCase.wantJSON {
				t.Errorf("json = %v, want %v", *jsonOutput, testCase.wantJSON)
			}
			if remaining := strings.Join(flags.Args(), " "); remaining != testCase.wantRemain {
				t.Errorf("positional = %q, want %q", remaining, testCase.wantRemain)
			}
		})
	}
}

func TestParseAnywhereReportsUnknownFlags(t *testing.T) {
	flags := (&app{err: io.Discard}).newFlagSet("test")
	flags.SetOutput(nopWriter{})
	flags.String("root", "", "root")
	if err := parseAnywhere(flags, []string{"name", "--nope", "x"}); err == nil {
		t.Fatal("expected an unknown flag to be reported rather than folded into the positional")
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestInstallPilotDocsNeverOverwritesWithoutForce(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("HEIKOU_HOME", filepath.Join(base, ".heikou"))

	dir, written, err := installPilotDocs(false)
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if len(written) != len(pilotDocs()) {
		t.Fatalf("wrote %d files, want %d", len(written), len(pilotDocs()))
	}
	for _, doc := range pilotDocs() {
		contents, err := os.ReadFile(filepath.Join(dir, doc.relative))
		if err != nil {
			t.Fatalf("read %q: %v", doc.relative, err)
		}
		if string(contents) != doc.contents {
			t.Fatalf("%q was not installed verbatim", doc.relative)
		}
	}

	// An edited instruction file belongs to the user and survives reinstallation.
	edited := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(edited, []byte("my own house rules\n"), 0o600); err != nil {
		t.Fatalf("edit AGENTS.md: %v", err)
	}
	if _, written, err = installPilotDocs(false); err != nil {
		t.Fatalf("second install: %v", err)
	}
	if len(written) != 0 {
		t.Fatalf("second install rewrote %v", written)
	}
	contents, err := os.ReadFile(edited)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "my own house rules\n" {
		t.Fatal("an edited AGENTS.md was overwritten")
	}

	// --force is the explicit way to pick up instructions from a newer binary.
	if _, written, err = installPilotDocs(true); err != nil {
		t.Fatalf("forced install: %v", err)
	}
	if len(written) != len(pilotDocs()) {
		t.Fatalf("forced install wrote %d files, want %d", len(written), len(pilotDocs()))
	}
	if contents, err = os.ReadFile(edited); err != nil {
		t.Fatal(err)
	}
	if string(contents) == "my own house rules\n" {
		t.Fatal("--force did not refresh AGENTS.md")
	}
}

// The instructions are the pilot's whole contract, so the rules that keep it
// from corrupting state or overclaiming must actually be present in what ships.
func TestPilotInstructionsCarryTheLoadBearingRules(t *testing.T) {
	combined := ""
	for _, doc := range pilotDocs() {
		combined += doc.contents
	}
	for _, required := range []string{
		"state.json",
		"h list --json",
		"--yes",
		"artifact_dir",
		"notes.md",
		"alternate screen",
	} {
		if !strings.Contains(combined, required) {
			t.Errorf("pilot instructions never mention %q", required)
		}
	}
	if !strings.Contains(combined, "Never edit `state.json` by hand") {
		t.Error("pilot instructions do not forbid hand-editing state")
	}
}

func TestPilotDocsInstallModeIsPrivate(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("HEIKOU_HOME", filepath.Join(base, ".heikou"))
	dir, _, err := installPilotDocs(false)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("AGENTS.md mode = %o, want 600", info.Mode().Perm())
	}
}

// A title or workstream name may legitimately begin with a dash. It must reach
// the command as text rather than being re-read as an unknown flag.
func TestParseAnywhereKeepsDashLeadingPositionals(t *testing.T) {
	flags := (&app{err: io.Discard}).newFlagSet("test")
	flags.SetOutput(nopWriter{})
	clear := flags.Bool("clear", false, "clear")
	if err := parseAnywhere(flags, []string{"a1b2", "--", "-fix the parser"}); err != nil {
		t.Fatalf("parseAnywhere: %v", err)
	}
	if *clear {
		t.Error("clear was set by a positional")
	}
	if got := strings.Join(flags.Args(), " "); got != "a1b2 -fix the parser" {
		t.Fatalf("positional = %q", got)
	}
}
