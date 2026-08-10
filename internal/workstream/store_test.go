package workstream

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStorePersistsVersionedAtomicState(t *testing.T) {
	base := t.TempDir()
	store := FileStore{Path: filepath.Join(base, "state", "state.json"), Artifacts: filepath.Join(base, "data")}
	now := time.Unix(1_700_000_000, 0).UTC()
	id := "018f0000-0000-4000-8000-000000000010"
	state, err := store.Mutate(context.Background(), func(state *State) (bool, error) {
		state.Workstreams = append(state.Workstreams, Workstream{
			ID: id, Name: "Heikou", ArtifactDir: filepath.Join(base, "data", id),
			Roots: []string{base}, Revision: 1, CreatedAt: now, UpdatedAt: now,
		})
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != StateVersion || state.Revision != 1 {
		t.Fatalf("unexpected state header: %#v", state)
	}
	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %o, want 600", info.Mode().Perm())
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Workstreams) != 1 || loaded.Workstreams[0].Name != "Heikou" {
		t.Fatalf("loaded state = %#v", loaded)
	}
	unchanged, err := store.Mutate(context.Background(), func(*State) (bool, error) { return false, nil })
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Revision != 1 {
		t.Fatalf("no-op mutation advanced revision to %d", unchanged.Revision)
	}
}

func TestValidateRejectsMultipleActiveMemberships(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	workstreamA := Workstream{ID: "018f0000-0000-4000-8000-000000000011", Name: "A", ArtifactDir: filepath.Join(root, "a"), Roots: []string{root}, Revision: 1, CreatedAt: now, UpdatedAt: now}
	workstreamB := Workstream{ID: "018f0000-0000-4000-8000-000000000012", Name: "B", ArtifactDir: filepath.Join(root, "b"), Roots: []string{root}, Revision: 1, CreatedAt: now, UpdatedAt: now}
	sessionID := "018f0000-0000-4000-8000-000000000013"
	state := State{
		Version: StateVersion, Workstreams: []Workstream{workstreamA, workstreamB},
		Sessions:    []SessionRecord{{ID: sessionID, Backend: "codex", InitialPrompt: "task", InitialRoot: root, CreatedAt: now, Launch: LaunchIntent{Status: LaunchPending}}},
		Memberships: []Membership{{WorkstreamID: workstreamA.ID, SessionID: sessionID, JoinedAt: now}, {WorkstreamID: workstreamB.ID, SessionID: sessionID, JoinedAt: now}},
	}
	if err := state.Validate(); err == nil {
		t.Fatal("multiple active memberships were accepted")
	}
}
