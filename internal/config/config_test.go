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
	if got := settings.ComposerKeys; got != (ComposerKeys{
		NewSession:  "enter",
		SendMessage: "tab",
		CycleRunner: "tab",
		CycleRoot:   "shift+tab",
	}) {
		t.Fatalf("composer keys = %#v", got)
	}
}

func TestLegacySettingsInheritComposerKeyDefaults(t *testing.T) {
	clearSettingsEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"default_runner":"claude"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := (Store{Path: path}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.NewSessionKey() != "enter" || settings.SendMessageKey() != "tab" ||
		settings.CycleRunnerKey() != "tab" || settings.CycleRootKey() != "shift+tab" {
		t.Fatalf("legacy composer defaults = %#v", settings.ComposerKeys)
	}
}

func TestSettingsLoadAndNormalizeComposerKeys(t *testing.T) {
	clearSettingsEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{
  "composer_keys": {
    "new_session": " SHIFT + CTRL + N ",
    "send_message": " Shift + Enter ",
    "cycle_runner": " F6 ",
    "cycle_root": " ALT + R "
  }
}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := (Store{Path: path}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := settings.ComposerKeys, (ComposerKeys{
		NewSession:  "ctrl+shift+n",
		SendMessage: "shift+enter",
		CycleRunner: "f6",
		CycleRoot:   "alt+r",
	}); got != want {
		t.Fatalf("composer keys = %#v, want %#v", got, want)
	}
	if settings.NewSessionKey() != "ctrl+shift+n" || settings.SendMessageKey() != "shift+enter" ||
		settings.CycleRunnerKey() != "f6" || settings.CycleRootKey() != "alt+r" {
		t.Fatalf("composer accessors disagree with loaded settings: %#v", settings.ComposerKeys)
	}
}

func TestPartialComposerKeysInheritDefaults(t *testing.T) {
	clearSettingsEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"composer_keys":{"new_session":"ctrl+n"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := (Store{Path: path}).Load()
	if err != nil {
		t.Fatal(err)
	}
	want := ComposerKeys{
		NewSession:  "ctrl+n",
		SendMessage: "tab",
		CycleRunner: "tab",
		CycleRoot:   "shift+tab",
	}
	if settings.ComposerKeys != want {
		t.Fatalf("composer keys = %#v, want %#v", settings.ComposerKeys, want)
	}
}

func TestComposerKeysMayBeSharedAcrossContexts(t *testing.T) {
	clearSettingsEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"composer_keys":{"new_session":"f6","send_message":"tab","cycle_runner":"tab","cycle_root":"f6"}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := (Store{Path: path}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.SendMessageKey() != settings.CycleRunnerKey() {
		t.Fatal("tab should be reusable between non-empty and empty composer contexts")
	}
	if settings.NewSessionKey() != settings.CycleRootKey() {
		t.Fatal("keys should be reusable between non-empty and empty composer contexts")
	}
}

func TestSettingsRejectInvalidComposerKeys(t *testing.T) {
	clearSettingsEnvironment(t)
	tests := map[string]string{
		"empty":                     `{"composer_keys":{"new_session":"  "}}`,
		"empty chord part":          `{"composer_keys":{"new_session":"ctrl++n"}}`,
		"internal whitespace":       `{"composer_keys":{"new_session":"page down"}}`,
		"reserved quit":             `{"composer_keys":{"new_session":"CTRL + C"}}`,
		"reserved navigation":       `{"composer_keys":{"cycle_runner":"up"}}`,
		"reserved editor":           `{"composer_keys":{"send_message":"backspace"}}`,
		"reserved lifecycle":        `{"composer_keys":{"new_session":"ctrl+x"}}`,
		"reserved help":             `{"composer_keys":{"cycle_root":"?"}}`,
		"reserved shifted help":     `{"composer_keys":{"cycle_root":"shift+/"}}`,
		"reserved f1 help":          `{"composer_keys":{"cycle_root":"f1"}}`,
		"empty runner enter":        `{"composer_keys":{"cycle_runner":"enter"}}`,
		"empty root enter":          `{"composer_keys":{"cycle_root":"enter"}}`,
		"nonempty context conflict": `{"composer_keys":{"new_session":" TAB "}}`,
		"empty context conflict":    `{"composer_keys":{"cycle_root":" TAB "}}`,
		"missing base key":          `{"composer_keys":{"new_session":"ctrl+shift"}}`,
		"multiple base keys":        `{"composer_keys":{"new_session":"ctrl+n+m"}}`,
		"repeated modifier":         `{"composer_keys":{"new_session":"ctrl+ctrl+n"}}`,
		"unsupported key name":      `{"composer_keys":{"new_session":"launch"}}`,
		"null key":                  `{"composer_keys":{"new_session":null}}`,
		"non-string key":            `{"composer_keys":{"new_session":6}}`,
		"unknown nested field":      `{"composer_keys":{"new_session":"f6","surprise":"f7"}}`,
		"wrong object shape":        `{"composer_keys":"enter"}`,
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := (Store{Path: path}).Load()
			if err == nil {
				t.Fatal("Load() error = nil")
			}
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("Load() error = %v; want config path", err)
			}
		})
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
		"null composer":    `{"composer_keys":null}`,
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
