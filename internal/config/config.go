package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zamborg/heikou/internal/heikou"
)

const (
	PathEnv          = "HEIKOU_CONFIG"
	DefaultRunnerEnv = "HEIKOU_DEFAULT_RUNNER"
	CodexBinaryEnv   = "HEIKOU_CODEX_BIN"
	ClaudeBinaryEnv  = "HEIKOU_CLAUDE_BIN"
)

// Config is intentionally the whole V0 settings model. Commands are argv
// arrays so flags remain data and never pass through a shell parser.
type Config struct {
	DefaultRunner heikou.Backend      `json:"default_runner"`
	Commands      map[string][]string `json:"commands"`
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
	}
}

func DefaultStore() (Store, error) {
	if value := strings.TrimSpace(os.Getenv(PathEnv)); value != "" {
		path, err := filepath.Abs(value)
		if err != nil {
			return Store{}, fmt.Errorf("resolve %s: %w", PathEnv, err)
		}
		return Store{Path: path}, nil
	}
	base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Store{}, fmt.Errorf("locate home directory: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	path, err := filepath.Abs(filepath.Join(base, "heikou", "config.json"))
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

func applyEnvironment(settings Config) (Config, error) {
	if value := strings.TrimSpace(os.Getenv(DefaultRunnerEnv)); value != "" {
		backend, err := heikou.ParseBackend(value)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", DefaultRunnerEnv, err)
		}
		settings.DefaultRunner = backend
	}
	for backend, name := range map[heikou.Backend]string{
		heikou.BackendCodex:  CodexBinaryEnv,
		heikou.BackendClaude: ClaudeBinaryEnv,
	} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
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
