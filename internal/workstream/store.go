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

	"github.com/zamborg/heikou/internal/env"
	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/home"
	"golang.org/x/sys/unix"
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
	statePath := env.Value(env.State)
	if statePath == "" {
		resolved, err := home.Path("state.json")
		if err != nil {
			return FileStore{}, err
		}
		statePath = resolved
	}
	statePath, err := filepath.Abs(statePath)
	if err != nil {
		return FileStore{}, fmt.Errorf("resolve state path: %w", err)
	}

	artifactBase := env.Value(env.Data)
	if artifactBase == "" {
		resolved, err := home.Path("workstreams")
		if err != nil {
			return FileStore{}, err
		}
		artifactBase = resolved
	}
	artifactBase, err = filepath.Abs(artifactBase)
	if err != nil {
		return FileStore{}, fmt.Errorf("resolve artifact path: %w", err)
	}
	return FileStore{Path: statePath, Artifacts: artifactBase}, nil
}

func (s FileStore) StatePath() string    { return s.Path }
func (s FileStore) ArtifactBase() string { return s.Artifacts }

// Exists reports whether durable state has ever been written. Reads do not
// create the file and no-op mutations do not write it, so this is false only
// for an installation that has never recorded anything — which makes it the
// honest signal for one-time setup, with no second marker to keep in sync.
func (s FileStore) Exists() bool {
	info, err := os.Stat(s.Path)
	return err == nil && !info.IsDir()
}

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

// RebaseArtifacts repoints workstream artifact directories that still live
// inside a previous artifact base. Artifact directories are persisted absolute,
// so relocating them on disk would otherwise strand every notes.md and artifact
// tree behind a path no longer present.
//
// It deliberately leaves Revision and UpdatedAt alone. Nothing about the
// workstream changed; only where Heikou keeps its files did, and claiming a
// domain edit for a relocation would be dishonest to anything reading revisions.
func (s FileStore) RebaseArtifacts(ctx context.Context, previousBase string) (int, error) {
	previousBase = strings.TrimSpace(previousBase)
	if previousBase == "" {
		return 0, nil
	}
	previousBase = filepath.Clean(previousBase)
	rebased := 0
	_, err := s.Mutate(ctx, func(state *State) (bool, error) {
		rebased = 0
		for index := range state.Workstreams {
			item := &state.Workstreams[index]
			relative, err := filepath.Rel(previousBase, filepath.Clean(item.ArtifactDir))
			if err != nil || relative == ".." ||
				strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
				filepath.IsAbs(relative) {
				continue
			}
			item.ArtifactDir = filepath.Join(s.Artifacts, relative)
			rebased++
		}
		return rebased > 0, nil
	})
	if err != nil {
		return 0, err
	}
	return rebased, nil
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

// stateV2 is the exact persisted v2 shape, kept for the same reason stateV1 is:
// a file claiming v2 must reject the v3 conversation field rather than absorb
// it, so that a state written by a newer Heikou cannot be silently downgraded
// and rewritten with the registration dropped.
type stateV2 struct {
	Version     int               `json:"version"`
	Revision    uint64            `json:"revision"`
	Workstreams []Workstream      `json:"workstreams"`
	Sessions    []sessionRecordV2 `json:"sessions"`
	Memberships []Membership      `json:"memberships"`
}

type sessionRecordV2 struct {
	ID            string         `json:"id"`
	Backend       heikou.Backend `json:"backend"`
	Title         string         `json:"title,omitempty"`
	InitialPrompt string         `json:"initial_prompt"`
	InitialRoot   string         `json:"initial_root"`
	CreatedAt     time.Time      `json:"created_at"`
	Launch        LaunchIntent   `json:"launch"`
	Outcome       *Outcome       `json:"outcome,omitempty"`
}

func (legacy stateV2) state() State {
	state := State{
		Version: legacy.Version, Revision: legacy.Revision,
		Workstreams: legacy.Workstreams, Memberships: legacy.Memberships,
		Sessions: make([]SessionRecord, 0, len(legacy.Sessions)),
	}
	for _, record := range legacy.Sessions {
		state.Sessions = append(state.Sessions, SessionRecord{
			ID: record.ID, Backend: record.Backend, Title: record.Title,
			InitialPrompt: record.InitialPrompt, InitialRoot: record.InitialRoot,
			CreatedAt: record.CreatedAt, Launch: record.Launch, Outcome: record.Outcome,
		})
	}
	return state
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
		var legacy stateV2
		if err := decodeStrictState(data, &legacy); err != nil {
			return State{}, err
		}
		return legacy.state(), nil
	case 3:
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
	{from: 2, to: 3, apply: migrateStateV2ToV3},
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

// migrateStateV2ToV3 adds the optional conversation registration. It is a
// schema-only transition: every existing session keeps a nil conversation
// rather than being back-filled.
//
// Back-filling would be possible for Claude — Heikou passed --session-id, so
// the durable id is the conversation id — and it is deliberately not done. A
// v2 record cannot distinguish a session that ran from one whose launch failed
// before Claude ever wrote a transcript, so back-filling would register
// conversations that never existed and present them as assigned facts. The
// registration is cheap to acquire on the next launch and worthless if it is
// not true.
func migrateStateV2ToV3(state State) (State, error) {
	state.Version = 3
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
