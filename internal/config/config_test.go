package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zamborg/heikou/internal/env"
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
		Reply:       "space",
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
	if settings.ReplyKey() != "space" ||
		settings.CycleRunnerKey() != "tab" || settings.CycleRootKey() != "shift+tab" {
		t.Fatalf("legacy composer defaults = %#v", settings.ComposerKeys)
	}
}

func TestSettingsLoadAndNormalizeComposerKeys(t *testing.T) {
	clearSettingsEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{
  "composer_keys": {
    "reply": " SHIFT + CTRL + N ",
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
		Reply:       "ctrl+shift+n",
		CycleRunner: "f6",
		CycleRoot:   "alt+r",
	}); got != want {
		t.Fatalf("composer keys = %#v, want %#v", got, want)
	}
	if settings.ReplyKey() != "ctrl+shift+n" ||
		settings.CycleRunnerKey() != "f6" || settings.CycleRootKey() != "alt+r" {
		t.Fatalf("composer accessors disagree with loaded settings: %#v", settings.ComposerKeys)
	}
}

func TestPartialComposerKeysInheritDefaults(t *testing.T) {
	clearSettingsEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"composer_keys":{"reply":"ctrl+n"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := (Store{Path: path}).Load()
	if err != nil {
		t.Fatal(err)
	}
	want := ComposerKeys{
		Reply:       "ctrl+n",
		CycleRunner: "tab",
		CycleRoot:   "shift+tab",
	}
	if settings.ComposerKeys != want {
		t.Fatalf("composer keys = %#v, want %#v", settings.ComposerKeys, want)
	}
}

// Every composer binding is now live at once — the cycle keys no longer wait
// for an empty composer — so two of them sharing a key makes one unreachable.
func TestComposerKeysRejectSharedBindings(t *testing.T) {
	clearSettingsEnvironment(t)
	for name, data := range map[string]string{
		"reply and runner": `{"composer_keys":{"reply":"f6","cycle_runner":"f6"}}`,
		"reply and root":   `{"composer_keys":{"reply":"f6","cycle_root":"f6"}}`,
		"runner and root":  `{"composer_keys":{"cycle_runner":"f6","cycle_root":"f6"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := (Store{Path: path}).Load()
			if err == nil {
				t.Fatal("Load() error = nil; want a conflict")
			}
			if !strings.Contains(err.Error(), "conflict on \"f6\"") {
				t.Fatalf("Load() error = %v; want a conflict naming the shared key", err)
			}
		})
	}
}

// new_session and send_message picked a commit key per destination. Enter is
// now the only commit key, so the fields must fail with a message that points
// at their replacement rather than a bare unknown-field error.
func TestRemovedComposerKeysNameTheirReplacement(t *testing.T) {
	clearSettingsEnvironment(t)
	for _, field := range []string{"new_session", "send_message"} {
		t.Run(field, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			data := `{"composer_keys":{"` + field + `":"enter"}}`
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := (Store{Path: path}).Load()
			if err == nil {
				t.Fatal("Load() error = nil; want a removal error")
			}
			for _, want := range []string{field, "removed", "reply", "space"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Load() error = %v; want it to mention %q", err, want)
				}
			}
		})
	}
}

func TestSettingsRejectInvalidComposerKeys(t *testing.T) {
	clearSettingsEnvironment(t)
	tests := map[string]string{
		"empty":                 `{"composer_keys":{"reply":"  "}}`,
		"empty chord part":      `{"composer_keys":{"reply":"ctrl++n"}}`,
		"internal whitespace":   `{"composer_keys":{"reply":"page down"}}`,
		"reserved quit":         `{"composer_keys":{"reply":"CTRL + C"}}`,
		"reserved navigation":   `{"composer_keys":{"cycle_runner":"up"}}`,
		"reserved editor":       `{"composer_keys":{"reply":"backspace"}}`,
		"reserved lifecycle":    `{"composer_keys":{"reply":"ctrl+x"}}`,
		"reserved archive":      `{"composer_keys":{"reply":"ctrl+v"}}`,
		"reserved resize mode":  `{"composer_keys":{"reply":"ctrl+g"}}`,
		"reserved help":         `{"composer_keys":{"cycle_root":"?"}}`,
		"reserved shifted help": `{"composer_keys":{"cycle_root":"shift+/"}}`,
		"reserved f1 help":      `{"composer_keys":{"cycle_root":"f1"}}`,
		"reserved commit key":   `{"composer_keys":{"reply":"enter"}}`,
		"runner enter":          `{"composer_keys":{"cycle_runner":"enter"}}`,
		"root enter":            `{"composer_keys":{"cycle_root":"enter"}}`,
		"reply conflict":        `{"composer_keys":{"reply":" TAB "}}`,
		"cycle conflict":        `{"composer_keys":{"cycle_root":" TAB "}}`,
		"missing base key":      `{"composer_keys":{"reply":"ctrl+shift"}}`,
		"multiple base keys":    `{"composer_keys":{"reply":"ctrl+n+m"}}`,
		"repeated modifier":     `{"composer_keys":{"reply":"ctrl+ctrl+n"}}`,
		"unsupported key name":  `{"composer_keys":{"reply":"launch"}}`,
		"null key":              `{"composer_keys":{"reply":null}}`,
		"non-string key":        `{"composer_keys":{"reply":6}}`,
		"unknown nested field":  `{"composer_keys":{"reply":"f6","surprise":"f7"}}`,
		"wrong object shape":    `{"composer_keys":"enter"}`,
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
	t.Setenv(env.DefaultRunner, "claude")
	t.Setenv(env.ClaudeBinary, "/custom/claude")
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
	for _, name := range []string{env.DefaultRunner, env.CodexBinary, env.ClaudeBinary} {
		t.Setenv(name, "")
	}
}

func loadBrief(t *testing.T, data string) (BriefConfig, error) {
	t.Helper()
	clearSettingsEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := (Store{Path: path}).Load()
	return settings.Brief, err
}

func TestBriefDefaultsWhenUnmentioned(t *testing.T) {
	brief, err := loadBrief(t, `{"default_runner":"claude"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(brief.Lead, ",") != "title,prompt,runner" || strings.Join(brief.Detail, ",") != "latest,prompt" {
		t.Fatalf("brief defaults = %#v", brief)
	}
	if len(brief.Sources) != 0 {
		t.Fatalf("default configuration declared sources: %#v", brief.Sources)
	}
}

// Omitting a slot inherits its default; writing an empty array is how a user
// asks for a title-only row. Those have to stay distinguishable.
func TestBriefEmptyDetailDiffersFromAnOmittedOne(t *testing.T) {
	omitted, err := loadBrief(t, `{"brief":{"lead":["title"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(omitted.Detail, ",") != "latest,prompt" {
		t.Fatalf("omitted detail did not inherit its default: %#v", omitted.Detail)
	}
	explicit, err := loadBrief(t, `{"brief":{"lead":["title"],"detail":[]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(explicit.Detail) != 0 {
		t.Fatalf("explicitly empty detail = %#v", explicit.Detail)
	}
}

func TestBriefRejectsMalformedLayouts(t *testing.T) {
	for name, test := range map[string]struct{ data, want string }{
		"unknown source":     {`{"brief":{"lead":["vibes"]}}`, `unknown source "vibes"`},
		"duplicate source":   {`{"brief":{"lead":["title","title"]}}`, `listed twice`},
		"empty lead":         {`{"brief":{"lead":[]}}`, `must name at least one source`},
		"empty name":         {`{"brief":{"lead":["  "]}}`, `cannot be empty`},
		"unknown field":      {`{"brief":{"led":["title"]}}`, `unknown field`},
		"null brief":         {`{"brief":null}`, `must be a JSON object`},
		"redefined builtin":  {`{"brief":{"sources":{"title":{"command":["x"]}}}}`, `built-in source`},
		"bad source name":    {`{"brief":{"sources":{"My Source":{"command":["x"]}}}}`, `lowercase letters`},
		"empty command":      {`{"brief":{"lead":["s"],"sources":{"s":{"command":[]}}}}`, `non-empty JSON array`},
		"unreferenced":       {`{"brief":{"sources":{"s":{"command":["x"]}}}}`, `not named in lead or detail`},
		"interval too large": {`{"brief":{"lead":["s"],"sources":{"s":{"command":["x"],"interval_seconds":99999}}}}`, `between 1 and 3600`},
		"timeout too small":  {`{"brief":{"lead":["s"],"sources":{"s":{"command":["x"],"timeout_seconds":0}}}}`, `between 1 and 60`},
		"timeout exceeds":    {`{"brief":{"lead":["s"],"sources":{"s":{"command":["x"],"interval_seconds":2,"timeout_seconds":30}}}}`, `cannot exceed interval_seconds`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadBrief(t, test.data)
			if err == nil {
				t.Fatalf("Load() error = nil; want %q", test.want)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v; want it to mention %q", err, test.want)
			}
		})
	}
}

// An unknown source name is the likeliest mistake in this block, so the error
// has to say what the valid names are rather than only that this one is wrong.
func TestBriefUnknownSourceErrorListsTheBuiltins(t *testing.T) {
	_, err := loadBrief(t, `{"brief":{"lead":["titel"]}}`)
	if err == nil {
		t.Fatal("Load() error = nil")
	}
	for _, name := range BuiltinBriefSources {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error does not offer %q as an alternative: %v", name, err)
		}
	}
	if !strings.Contains(err.Error(), "brief.sources") {
		t.Fatalf("error does not say where a custom source is defined: %v", err)
	}
}

func TestBriefAcceptsACommandSource(t *testing.T) {
	brief, err := loadBrief(t, `{"brief":{
		"lead":["title","prompt","runner"],
		"detail":["status","latest"],
		"sources":{"status":{"command":["agent-status","--porcelain"],"interval_seconds":5,"timeout_seconds":2}}
	}}`)
	if err != nil {
		t.Fatal(err)
	}
	source, ok := brief.Sources["status"]
	if !ok {
		t.Fatalf("sources = %#v", brief.Sources)
	}
	if strings.Join(source.Command, " ") != "agent-status --porcelain" {
		t.Fatalf("command = %#v", source.Command)
	}
	if source.IntervalSeconds != 5 || source.TimeoutSeconds != 2 {
		t.Fatalf("bounds = %#v", source)
	}
}

func TestBriefCommandSourceBoundsDefaultWhenOmitted(t *testing.T) {
	brief, err := loadBrief(t, `{"brief":{"detail":["status"],"sources":{"status":{"command":["agent-status"]}}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if source := brief.Sources["status"]; source.IntervalSeconds != 10 || source.TimeoutSeconds != 3 {
		t.Fatalf("defaulted bounds = %#v", source)
	}
}
