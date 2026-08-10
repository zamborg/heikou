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
	lock, err := s.lock(ctx, unix.LOCK_SH)
	if err != nil {
		return State{}, err
	}
	defer unlock(lock)
	return s.loadUnlocked()
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

	state, err := s.loadUnlocked()
	if err != nil {
		return State{}, err
	}
	changed, err := mutate(&state)
	if err != nil {
		return State{}, err
	}
	if !changed {
		return state, nil
	}
	state.Version = StateVersion
	state.Revision++
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

func (s FileStore) loadUnlocked() (State, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return EmptyState(), nil
		}
		return State{}, fmt.Errorf("read state %q: %w", s.Path, err)
	}
	var state State
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return State{}, fmt.Errorf("parse state %q: %w", s.Path, err)
	}
	if err := requireStateEOF(decoder); err != nil {
		return State{}, fmt.Errorf("parse state %q: %w", s.Path, err)
	}
	if err := state.Validate(); err != nil {
		return State{}, fmt.Errorf("validate state %q: %w", s.Path, err)
	}
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
