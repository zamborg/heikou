package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/zamborg/heikou/internal/env"
	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/home"
)

// Config is intentionally the whole V0 settings model. Commands are argv
// arrays so flags remain data and never pass through a shell parser.
type Config struct {
	DefaultRunner heikou.Backend      `json:"default_runner"`
	Commands      map[string][]string `json:"commands"`
	ComposerKeys  ComposerKeys        `json:"composer_keys"`
}

// ComposerKeys controls the composer bindings whose target is chosen before
// typing rather than at the moment of commit. Reply aims the composer at the
// selected session while the composer is still empty; CycleRunner and CycleRoot
// change what the next launch uses. Enter is the single commit key and is not
// configurable, so the destination is always the one the composer displays.
type ComposerKeys struct {
	Reply       string `json:"reply"`
	CycleRunner string `json:"cycle_runner"`
	CycleRoot   string `json:"cycle_root"`
}

type Store struct {
	Path string
}

func Default() Config {
	return Config{
		DefaultRunner: heikou.BackendCodex,
		Commands: map[string][]string{
			string(heikou.BackendCodex):  {"codex"},
			string(heikou.BackendClaude): {"claude"},
		},
		ComposerKeys: ComposerKeys{
			Reply:       "space",
			CycleRunner: "tab",
			CycleRoot:   "shift+tab",
		},
	}
}

func DefaultStore() (Store, error) {
	if value := env.Value(env.Config); value != "" {
		path, err := filepath.Abs(value)
		if err != nil {
			return Store{}, fmt.Errorf("resolve %s: %w", env.Config, err)
		}
		return Store{Path: path}, nil
	}
	target, err := home.Path("config.json")
	if err != nil {
		return Store{}, err
	}
	path, err := filepath.Abs(target)
	if err != nil {
		return Store{}, fmt.Errorf("resolve config path: %w", err)
	}
	return Store{Path: path}, nil
}

func (s Store) Load() (Config, error) {
	settings := Default()
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("read settings %q: %w", s.Path, err)
		}
		return applyEnvironment(settings)
	}

	var disk struct {
		DefaultRunner string              `json:"default_runner"`
		Commands      map[string][]string `json:"commands"`
		ComposerKeys  json.RawMessage     `json:"composer_keys"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&disk); err != nil {
		return Config{}, fmt.Errorf("parse settings %q: %w", s.Path, err)
	}
	if err := requireEOF(decoder); err != nil {
		return Config{}, fmt.Errorf("parse settings %q: %w", s.Path, err)
	}
	if strings.TrimSpace(disk.DefaultRunner) != "" {
		backend, err := heikou.ParseBackend(disk.DefaultRunner)
		if err != nil {
			return Config{}, fmt.Errorf("parse settings %q: default_runner: %w", s.Path, err)
		}
		settings.DefaultRunner = backend
	}
	for name, command := range disk.Commands {
		backend, err := heikou.ParseBackend(name)
		if err != nil {
			return Config{}, fmt.Errorf("parse settings %q: commands.%s: %w", s.Path, name, err)
		}
		if backend == heikou.BackendNoAgent {
			return Config{}, fmt.Errorf("parse settings %q: commands.no-agent is not allowed; no-agent always starts tmux's default shell", s.Path)
		}
		if err := validateCommand(command); err != nil {
			return Config{}, fmt.Errorf("parse settings %q: commands.%s: %w", s.Path, name, err)
		}
		settings.Commands[string(backend)] = append([]string(nil), command...)
	}
	if len(disk.ComposerKeys) > 0 {
		composerKeys, err := decodeComposerKeys(disk.ComposerKeys)
		if err != nil {
			return Config{}, fmt.Errorf("parse settings %q: composer_keys: %w", s.Path, err)
		}
		if err := composerKeys.apply(&settings.ComposerKeys); err != nil {
			return Config{}, fmt.Errorf("parse settings %q: composer_keys: %w", s.Path, err)
		}
	}
	return applyEnvironment(settings)
}

func (s Store) Exists() bool {
	_, err := os.Stat(s.Path)
	return err == nil
}

// Ensure creates an editable default file only when settings are explicitly
// opened for editing. Existing files, including invalid ones, are never
// overwritten.
func (s Store) Ensure() error {
	if _, err := os.Stat(s.Path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect settings %q: %w", s.Path, err)
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	data, err := json.MarshalIndent(Default(), "", "  ")
	if err != nil {
		return fmt.Errorf("encode default settings: %w", err)
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(s.Path), ".config-*.json")
	if err != nil {
		return fmt.Errorf("create settings temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect settings temporary file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write settings temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync settings temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close settings temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, s.Path); err != nil {
		return fmt.Errorf("install settings file: %w", err)
	}
	return nil
}

func (c Config) Command(backend heikou.Backend) []string {
	return append([]string(nil), c.Commands[string(backend)]...)
}

// ReplyKey returns the normalized key that aims the empty composer at the
// selected live session.
func (c Config) ReplyKey() string {
	return c.ComposerKeys.Reply
}

// CycleRunnerKey returns the normalized key used to cycle the runner for the
// next launch.
func (c Config) CycleRunnerKey() string {
	return c.ComposerKeys.CycleRunner
}

// CycleRootKey returns the normalized key used to cycle the launch root for the
// next launch.
func (c Config) CycleRootKey() string {
	return c.ComposerKeys.CycleRoot
}

// optionalComposerKeysJSON preserves the distinction between an omitted key,
// which inherits its default, and an explicitly empty key, which is invalid.
type optionalComposerKeysJSON struct {
	Reply       optionalKeyJSON `json:"reply"`
	CycleRunner optionalKeyJSON `json:"cycle_runner"`
	CycleRoot   optionalKeyJSON `json:"cycle_root"`

	// These two selected a commit key per destination. Enter is now the only
	// commit key, so they are decoded solely to fail with a message that names
	// their replacement instead of a bare unknown-field error.
	NewSession  optionalKeyJSON `json:"new_session"`
	SendMessage optionalKeyJSON `json:"send_message"`
}

type optionalKeyJSON struct {
	value string
	set   bool
}

func (key *optionalKeyJSON) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("key name must be a string")
	}
	if err := json.Unmarshal(data, &key.value); err != nil {
		return errors.New("key name must be a string")
	}
	key.set = true
	return nil
}

func decodeComposerKeys(data []byte) (optionalComposerKeysJSON, error) {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return optionalComposerKeysJSON{}, errors.New("must be a JSON object")
	}
	var keys optionalComposerKeysJSON
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&keys); err != nil {
		return optionalComposerKeysJSON{}, err
	}
	if err := requireEOF(decoder); err != nil {
		return optionalComposerKeysJSON{}, err
	}
	return keys, nil
}

func (keys optionalComposerKeysJSON) apply(target *ComposerKeys) error {
	for _, removed := range []struct {
		name  string
		value optionalKeyJSON
	}{
		{name: "new_session", value: keys.NewSession},
		{name: "send_message", value: keys.SendMessage},
	} {
		if removed.value.set {
			return fmt.Errorf("%s: removed; Enter now commits to whichever destination the composer shows. "+
				"Delete this field, and use \"reply\" to choose the key that aims the composer at the selected session (default \"space\")", removed.name)
		}
	}
	values := []struct {
		name   string
		value  optionalKeyJSON
		target *string
	}{
		{name: "reply", value: keys.Reply, target: &target.Reply},
		{name: "cycle_runner", value: keys.CycleRunner, target: &target.CycleRunner},
		{name: "cycle_root", value: keys.CycleRoot, target: &target.CycleRoot},
	}
	for _, item := range values {
		if !item.value.set {
			continue
		}
		normalized, err := normalizeKeyName(item.value.value)
		if err != nil {
			return fmt.Errorf("%s: %w", item.name, err)
		}
		if _, reserved := reservedComposerKeys[normalized]; reserved {
			return fmt.Errorf("%s: key %q is reserved by Heikou", item.name, normalized)
		}
		*item.target = normalized
	}
	for _, pair := range [][2]int{{0, 1}, {0, 2}, {1, 2}} {
		first, second := values[pair[0]], values[pair[1]]
		if *first.target == *second.target {
			return fmt.Errorf("%s and %s conflict on %q", first.name, second.name, *first.target)
		}
	}
	return nil
}

func normalizeKeyName(value string) (string, error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(value)), "+")
	modifierOrder := []string{"ctrl", "alt", "shift", "meta", "hyper", "super"}
	modifiers := make(map[string]bool, len(modifierOrder))
	base := ""
	for _, raw := range parts {
		part := strings.TrimSpace(raw)
		if part == "" {
			return "", errors.New("key name must be non-empty")
		}
		if strings.IndexFunc(part, func(character rune) bool {
			return character <= ' ' || character == 0x7f
		}) >= 0 {
			return "", errors.New("key name cannot contain whitespace or control characters")
		}
		if slicesContains(modifierOrder, part) {
			if modifiers[part] {
				return "", fmt.Errorf("modifier %q is repeated", part)
			}
			modifiers[part] = true
			continue
		}
		if base != "" {
			return "", errors.New("key chord must contain exactly one non-modifier key")
		}
		base = part
	}
	if base == "" {
		return "", errors.New("key chord must contain a non-modifier key")
	}
	if !supportedKeyName(base) {
		return "", fmt.Errorf("unsupported key name %q", base)
	}
	canonical := make([]string, 0, len(modifiers)+1)
	for _, modifier := range modifierOrder {
		if modifiers[modifier] {
			canonical = append(canonical, modifier)
		}
	}
	return strings.Join(append(canonical, base), "+"), nil
}

func slicesContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func supportedKeyName(value string) bool {
	if utf8.RuneCountInString(value) == 1 {
		character, _ := utf8.DecodeRuneInString(value)
		return unicode.IsPrint(character) && character != '+'
	}
	if _, ok := namedComposerKeys[value]; ok {
		return true
	}
	if strings.HasPrefix(value, "f") {
		number, err := strconv.Atoi(strings.TrimPrefix(value, "f"))
		return err == nil && number >= 1 && number <= 63
	}
	return false
}

var namedComposerKeys = map[string]struct{}{
	"backspace": {}, "begin": {}, "delete": {}, "down": {}, "end": {}, "enter": {}, "esc": {},
	"find": {}, "home": {}, "insert": {}, "left": {}, "pgdown": {}, "pgup": {}, "right": {},
	"select": {}, "space": {}, "tab": {}, "up": {},
}

// These keys already have dashboard-wide navigation, editing, lifecycle, or
// panel behavior. Letting a composer binding claim one would make one of the
// two actions unreachable. Question mark is included for the help panel, and
// Enter because it is the single commit key for every composer destination.
var reservedComposerKeys = map[string]struct{}{
	"?":         {},
	"backspace": {},
	"ctrl+a":    {},
	"enter":     {},
	"ctrl+c":    {},
	"ctrl+e":    {},
	"ctrl+g":    {},
	"ctrl+h":    {},
	"ctrl+r":    {},
	"ctrl+s":    {},
	"ctrl+u":    {},
	"ctrl+w":    {},
	"ctrl+x":    {},
	"delete":    {},
	"down":      {},
	"end":       {},
	"esc":       {},
	"f1":        {},
	"f2":        {},
	"f3":        {},
	"home":      {},
	"left":      {},
	"pgdown":    {},
	"pgup":      {},
	"right":     {},
	"shift+/":   {},
	"up":        {},
}

func applyEnvironment(settings Config) (Config, error) {
	if value := env.Value(env.DefaultRunner); value != "" {
		backend, err := heikou.ParseBackend(value)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", env.DefaultRunner, err)
		}
		settings.DefaultRunner = backend
	}
	for backend, name := range map[heikou.Backend]string{
		heikou.BackendCodex:  env.CodexBinary,
		heikou.BackendClaude: env.ClaudeBinary,
	} {
		if value := env.Value(name); value != "" {
			command := settings.Command(backend)
			if len(command) == 0 {
				command = []string{value}
			} else {
				command[0] = value
			}
			settings.Commands[string(backend)] = command
		}
	}
	return settings, nil
}

func validateCommand(command []string) error {
	if len(command) == 0 {
		return errors.New("command must be a non-empty JSON array")
	}
	if strings.TrimSpace(command[0]) == "" {
		return errors.New("command executable cannot be empty")
	}
	for _, value := range command {
		if strings.IndexByte(value, 0) >= 0 {
			return errors.New("command arguments cannot contain NUL bytes")
		}
	}
	return nil
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("unexpected data after the JSON object")
	}
	return err
}
