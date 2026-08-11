package workstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zamborg/heikou/internal/heikou"
	"golang.org/x/sys/unix"
)

const (
	StatePathEnv = "HEIKOU_STATE"
	DataPathEnv  = "HEIKOU_DATA"
)

type Repository interface {
	Load(context.Context) (State, error)
	Mutate(context.Context, func(*State) (bool, error)) (State, error)
	WithLifecycleLock(context.Context, func() error) error
	StatePath() string
	ArtifactBase() string
}

type FileStore struct {
	Path      string
	Artifacts string
}

func DefaultStore() (FileStore, error) {
	statePath := strings.TrimSpace(os.Getenv(StatePathEnv))
	if statePath == "" {
		base := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return FileStore{}, fmt.Errorf("locate home directory: %w", err)
			}
			base = filepath.Join(home, ".local", "state")
		}
		statePath = filepath.Join(base, "heikou", "state.json")
	}
	statePath, err := filepath.Abs(statePath)
	if err != nil {
		return FileStore{}, fmt.Errorf("resolve state path: %w", err)
	}

	artifactBase := strings.TrimSpace(os.Getenv(DataPathEnv))
	if artifactBase == "" {
		base := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return FileStore{}, fmt.Errorf("locate home directory: %w", err)
			}
			base = filepath.Join(home, ".local", "share")
		}
		artifactBase = filepath.Join(base, "heikou", "workstreams")
	}
	artifactBase, err = filepath.Abs(artifactBase)
	if err != nil {
		return FileStore{}, fmt.Errorf("resolve artifact path: %w", err)
	}
	return FileStore{Path: statePath, Artifacts: artifactBase}, nil
}

func (s FileStore) StatePath() string    { return s.Path }
func (s FileStore) ArtifactBase() string { return s.Artifacts }

func (s FileStore) Load(ctx context.Context) (State, error) {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return State{}, fmt.Errorf("create state directory: %w", err)
	}
	// Loading an older valid state also installs its migrated representation.
	// Take the exclusive lock so that migration is a single read/replace action
	// and another process can never observe a partially upgraded file.
	lock, err := s.lock(ctx, unix.LOCK_EX)
	if err != nil {
		return State{}, err
	}
	defer unlock(lock)
	state, migrated, err := s.loadUnlocked()
	if err != nil {
		return State{}, err
	}
	if migrated {
		if err := s.writeUnlocked(state); err != nil {
			return State{}, fmt.Errorf("persist migrated state: %w", err)
		}
	}
	return state, nil
}

func (s FileStore) Mutate(ctx context.Context, mutate func(*State) (bool, error)) (State, error) {
	if mutate == nil {
		return State{}, errors.New("state mutation is required")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return State{}, fmt.Errorf("create state directory: %w", err)
	}
	lock, err := s.lock(ctx, unix.LOCK_EX)
	if err != nil {
		return State{}, err
	}
	defer unlock(lock)

	state, migrated, err := s.loadUnlocked()
	if err != nil {
		return State{}, err
	}
	changed, err := mutate(&state)
	if err != nil {
		return State{}, err
	}
	if !changed && !migrated {
		return state, nil
	}
	if changed {
		state.Version = StateVersion
		state.Revision++
	}
	if err := state.Validate(); err != nil {
		return State{}, fmt.Errorf("validate state mutation: %w", err)
	}
	if err := s.writeUnlocked(state); err != nil {
		return State{}, err
	}
	return state, nil
}

// WithLifecycleLock serializes operations that cross the durable store and
// tmux, such as launching or deleting a session. It uses a separate lock from
// state reads/writes so those operations may still call Load and Mutate.
func (s FileStore) WithLifecycleLock(ctx context.Context, operation func() error) error {
	if operation == nil {
		return errors.New("lifecycle operation is required")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	lock, err := lockFile(ctx, s.Path+".lifecycle.lock", unix.LOCK_EX)
	if err != nil {
		return err
	}
	defer unlock(lock)
	return operation()
}

func (s FileStore) loadUnlocked() (State, bool, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return EmptyState(), false, nil
		}
		return State{}, false, fmt.Errorf("read state %q: %w", s.Path, err)
	}
	state, err := decodeStoredState(data)
	if err != nil {
		return State{}, false, fmt.Errorf("parse state %q: %w", s.Path, err)
	}
	state, migrated, err := migrateState(state)
	if err != nil {
		return State{}, false, fmt.Errorf("validate state %q: %w", s.Path, err)
	}
	return state, migrated, nil
}

// stateV1 is the exact persisted v1 shape. Keeping an explicit decoder for an
// old version means a field introduced in v2 remains an unknown field when a
// file still claims to be v1.
type stateV1 struct {
	Version     int               `json:"version"`
	Revision    uint64            `json:"revision"`
	Workstreams []Workstream      `json:"workstreams"`
	Sessions    []sessionRecordV1 `json:"sessions"`
	Memberships []Membership      `json:"memberships"`
}

type sessionRecordV1 struct {
	ID            string         `json:"id"`
	Backend       heikou.Backend `json:"backend"`
	InitialPrompt string         `json:"initial_prompt"`
	InitialRoot   string         `json:"initial_root"`
	CreatedAt     time.Time      `json:"created_at"`
	Launch        LaunchIntent   `json:"launch"`
	Outcome       *Outcome       `json:"outcome,omitempty"`
}

func (legacy stateV1) state() State {
	state := State{
		Version: legacy.Version, Revision: legacy.Revision,
		Workstreams: legacy.Workstreams, Memberships: legacy.Memberships,
		Sessions: make([]SessionRecord, 0, len(legacy.Sessions)),
	}
	for _, record := range legacy.Sessions {
		state.Sessions = append(state.Sessions, SessionRecord{
			ID: record.ID, Backend: record.Backend, InitialPrompt: record.InitialPrompt,
			InitialRoot: record.InitialRoot, CreatedAt: record.CreatedAt,
			Launch: record.Launch, Outcome: record.Outcome,
		})
	}
	return state
}

func decodeStoredState(data []byte) (State, error) {
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return State{}, err
	}
	if header.Version <= 0 {
		return State{}, fmt.Errorf("invalid state version %d", header.Version)
	}
	if header.Version > StateVersion {
		return State{}, fmt.Errorf("state version %d is newer than supported version %d", header.Version, StateVersion)
	}

	switch header.Version {
	case 1:
		var legacy stateV1
		if err := decodeStrictState(data, &legacy); err != nil {
			return State{}, err
		}
		return legacy.state(), nil
	case 2:
		var state State
		if err := decodeStrictState(data, &state); err != nil {
			return State{}, err
		}
		return state, nil
	default:
		return State{}, fmt.Errorf("state version %d has no decoder", header.Version)
	}
}

func decodeStrictState(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return requireStateEOF(decoder)
}

type stateMigration struct {
	from  int
	to    int
	apply func(State) (State, error)
}

var orderedStateMigrations = []stateMigration{
	{from: 1, to: 2, apply: migrateStateV1ToV2},
}

func migrateState(state State) (State, bool, error) {
	if state.Version <= 0 {
		return State{}, false, fmt.Errorf("invalid state version %d", state.Version)
	}
	if state.Version > StateVersion {
		return State{}, false, fmt.Errorf("state version %d is newer than supported version %d", state.Version, StateVersion)
	}
	migrated := false
	for state.Version < StateVersion {
		migration, ok := migrationFrom(state.Version)
		if !ok {
			return State{}, false, fmt.Errorf("no migration from state version %d", state.Version)
		}
		if migration.to != migration.from+1 {
			return State{}, false, fmt.Errorf("state migration %d to %d is not an adjacent ordered transition", migration.from, migration.to)
		}
		if err := state.validateVersion(migration.from); err != nil {
			return State{}, false, fmt.Errorf("validate state v%d before migration: %w", migration.from, err)
		}
		previousRevision := state.Revision
		next, err := migration.apply(state)
		if err != nil {
			return State{}, false, fmt.Errorf("migrate state v%d to v%d: %w", migration.from, migration.to, err)
		}
		if next.Version != migration.to {
			return State{}, false, fmt.Errorf("state migration %d to %d produced version %d", migration.from, migration.to, next.Version)
		}
		if next.Revision != previousRevision {
			return State{}, false, fmt.Errorf("state migration %d to %d changed domain revision from %d to %d", migration.from, migration.to, previousRevision, next.Revision)
		}
		state, migrated = next, true
	}
	if err := state.Validate(); err != nil {
		return State{}, false, err
	}
	return state, migrated, nil
}

func migrationFrom(version int) (stateMigration, bool) {
	for _, migration := range orderedStateMigrations {
		if migration.from == version {
			return migration, true
		}
	}
	return stateMigration{}, false
}

func migrateStateV1ToV2(state State) (State, error) {
	state.Version = 2
	return state, nil
}

func (s FileStore) writeUnlocked(state State) error {
	directory := filepath.Dir(s.Path)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(directory, ".state-*.json")
	if err != nil {
		return fmt.Errorf("create state temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect state temporary file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write state temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync state temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close state temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, s.Path); err != nil {
		return fmt.Errorf("install state file: %w", err)
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open state directory for sync: %w", err)
	}
	defer directoryHandle.Close()
	if err := directoryHandle.Sync(); err != nil {
		return fmt.Errorf("sync state directory: %w", err)
	}
	return nil
}

func (s FileStore) lock(ctx context.Context, kind int) (*os.File, error) {
	return lockFile(ctx, s.Path+".lock", kind)
}

func lockFile(ctx context.Context, path string, kind int) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open state lock %q: %w", path, err)
	}
	for {
		if err := unix.Flock(int(file.Fd()), kind|unix.LOCK_NB); err == nil {
			return file, nil
		} else if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("lock state %q: %w", path, err)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, fmt.Errorf("lock state %q: %w", path, ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func unlock(file *os.File) {
	_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
	_ = file.Close()
}

func requireStateEOF(decoder *json.Decoder) error {
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
