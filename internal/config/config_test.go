package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zamborg/heikou/internal/heikou"
)

func TestMissingSettingsUseDefaults(t *testing.T) {
	clearSettingsEnvironment(t)
	store := Store{Path: filepath.Join(t.TempDir(), "missing.json")}
	settings, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.DefaultRunner != heikou.BackendCodex {
		t.Fatalf("default runner = %q", settings.DefaultRunner)
	}
	if got := settings.Command(heikou.BackendClaude); !slices.Equal(got, []string{"claude"}) {
		t.Fatalf("claude command = %#v", got)
	}
}

func TestSettingsLoadArgvWithoutShellInterpretation(t *testing.T) {
	clearSettingsEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{
  "default_runner": "no-agent",
  "commands": {
    "codex": ["/a path/codex", "--flag", "", "$(touch nope)", "日本語"],
    "claude": ["claude", "--dangerously-skip-permissions"]
  }
}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := (Store{Path: path}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.DefaultRunner != heikou.BackendNoAgent {
		t.Fatalf("default runner = %q", settings.DefaultRunner)
	}
	want := []string{"/a path/codex", "--flag", "", "$(touch nope)", "日本語"}
	if got := settings.Command(heikou.BackendCodex); !slices.Equal(got, want) {
		t.Fatalf("codex command = %#v, want %#v", got, want)
	}
}

func TestSettingsRejectMalformedShapes(t *testing.T) {
	clearSettingsEnvironment(t)
	tests := map[string]string{
		"string command":   `{"commands":{"codex":"codex --flag"}}`,
		"empty command":    `{"commands":{"codex":[]}}`,
		"empty executable": `{"commands":{"codex":["  "]}}`,
		"unknown field":    `{"surprise":true}`,
		"unknown runner":   `{"commands":{"other":["tool"]}}`,
		"no-agent command": `{"commands":{"no-agent":["zsh"]}}`,
		"trailing object":  `{} {}`,
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := (Store{Path: path}).Load(); err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("Load() error = %v; want config path", err)
			}
		})
	}
}

func TestEnvironmentOverridesExecutableAndDefaultOnly(t *testing.T) {
	clearSettingsEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"default_runner":"codex","commands":{"claude":["claude","--flag"]}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(DefaultRunnerEnv, "claude")
	t.Setenv(ClaudeBinaryEnv, "/custom/claude")
	settings, err := (Store{Path: path}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.DefaultRunner != heikou.BackendClaude {
		t.Fatalf("default runner = %q", settings.DefaultRunner)
	}
	want := []string{"/custom/claude", "--flag"}
	if got := settings.Command(heikou.BackendClaude); !slices.Equal(got, want) {
		t.Fatalf("claude command = %#v, want %#v", got, want)
	}
}

func TestEnsureCreatesPrivateEditableJSON(t *testing.T) {
	clearSettingsEnvironment(t)
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	store := Store{Path: path}
	if err := store.Ensure(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
}

func clearSettingsEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{DefaultRunnerEnv, CodexBinaryEnv, ClaudeBinaryEnv} {
		t.Setenv(name, "")
	}
}
