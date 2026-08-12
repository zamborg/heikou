package workstream

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestFileStorePreservesWorkstreamArrayOrder(t *testing.T) {
	base := t.TempDir()
	store := FileStore{Path: filepath.Join(base, "state.json"), Artifacts: filepath.Join(base, "data")}
	now := time.Unix(1_700_000_100, 0).UTC()
	ids := []string{
		"018f0000-0000-4000-8000-000000000021",
		"018f0000-0000-4000-8000-000000000022",
		"018f0000-0000-4000-8000-000000000023",
	}
	_, err := store.Mutate(context.Background(), func(state *State) (bool, error) {
		for index, id := range ids {
			state.Workstreams = append(state.Workstreams, Workstream{
				ID: id, Name: string(rune('A' + index)), ArtifactDir: filepath.Join(base, "data", id),
				Roots: []string{base}, Revision: 1, CreatedAt: now, UpdatedAt: now,
			})
		}
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Mutate(context.Background(), func(state *State) (bool, error) {
		state.Workstreams[0], state.Workstreams[2] = state.Workstreams[2], state.Workstreams[0]
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{ids[2], ids[1], ids[0]}
	for index, id := range want {
		if loaded.Workstreams[index].ID != id {
			t.Fatalf("workstream order[%d] = %q, want %q", index, loaded.Workstreams[index].ID, id)
		}
	}
}

func TestLifecycleLockSerializesFileStoreInstances(t *testing.T) {
	base := t.TempDir()
	first := FileStore{Path: filepath.Join(base, "state.json"), Artifacts: filepath.Join(base, "data")}
	second := FileStore{Path: first.Path, Artifacts: first.Artifacts}
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.WithLifecycleLock(context.Background(), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- second.WithLifecycleLock(context.Background(), func() error {
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
		t.Fatal("second lifecycle operation entered before the first released its lock")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second lifecycle operation did not enter after release")
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
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

func TestFileStoreLoadMigratesV1FixtureWithoutChangingDomainRevision(t *testing.T) {
	store := storeFromFixture(t, "state-v1-valid.json")
	state, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != 2 || state.Revision != 9 {
		t.Fatalf("migrated header = version %d revision %d, want version 2 revision 9", state.Version, state.Revision)
	}
	if len(state.Sessions) != 2 || state.Sessions[0].Title != "" || state.Sessions[0].InitialPrompt != "Preserve this launch identity" {
		t.Fatalf("migrated sessions = %#v", state.Sessions)
	}
	completed := state.Sessions[1]
	if completed.Launch.Binding == nil || completed.Launch.Binding.Socket != "heikou-v1" ||
		completed.Outcome == nil || completed.Outcome.ExitCode == nil || *completed.Outcome.ExitCode != 7 {
		t.Fatalf("migrated completed session = %#v", completed)
	}

	persisted := readPersistedState(t, store.Path)
	if persisted.Version != 2 || persisted.Revision != 9 {
		t.Fatalf("persisted migrated header = version %d revision %d", persisted.Version, persisted.Revision)
	}
	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("migrated state mode = %o, want 600", info.Mode().Perm())
	}
}

func TestFileStoreMutateMigratesV1FixtureAndCountsOnlyDomainChange(t *testing.T) {
	t.Run("no-op", func(t *testing.T) {
		store := storeFromFixture(t, "state-v1-valid.json")
		state, err := store.Mutate(context.Background(), func(*State) (bool, error) { return false, nil })
		if err != nil {
			t.Fatal(err)
		}
		if state.Version != 2 || state.Revision != 9 {
			t.Fatalf("no-op migration header = version %d revision %d", state.Version, state.Revision)
		}
		persisted := readPersistedState(t, store.Path)
		if persisted.Version != 2 || persisted.Revision != 9 {
			t.Fatalf("persisted no-op migration header = version %d revision %d", persisted.Version, persisted.Revision)
		}
	})

	t.Run("domain mutation", func(t *testing.T) {
		store := storeFromFixture(t, "state-v1-valid.json")
		state, err := store.Mutate(context.Background(), func(state *State) (bool, error) {
			state.Workstreams[0].Description = "changed after migration"
			state.Workstreams[0].Revision++
			state.Workstreams[0].UpdatedAt = state.Workstreams[0].UpdatedAt.Add(time.Second)
			return true, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if state.Version != 2 || state.Revision != 10 {
			t.Fatalf("mutated migration header = version %d revision %d, want version 2 revision 10", state.Version, state.Revision)
		}
	})
}

func TestFileStoreLoadsV2TitleFixture(t *testing.T) {
	store := storeFromFixture(t, "state-v2-valid.json")
	state, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != StateVersion || state.Revision != 12 || len(state.Sessions) != 1 {
		t.Fatalf("loaded v2 state = %#v", state)
	}
	if state.Sessions[0].Title != "Release Linux build" {
		t.Fatalf("loaded title = %q", state.Sessions[0].Title)
	}
}

func TestFileStoreRejectsInvalidVersionedFixturesWithoutRewriting(t *testing.T) {
	tests := []struct {
		fixture string
		want    string
	}{
		{fixture: "state-v1-rejects-v2-field.json", want: `unknown field "title"`},
		{fixture: "state-v1-invalid.json", want: "invalid launch metadata"},
		{fixture: "state-v2-unknown-field.json", want: `unknown field "surprise"`},
		{fixture: "state-v3-future.json", want: "newer than supported version 2"},
	}
	for _, test := range tests {
		t.Run(test.fixture, func(t *testing.T) {
			store := storeFromFixture(t, test.fixture)
			before, err := os.ReadFile(store.Path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(context.Background()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want substring %q", err, test.want)
			}
			after, err := os.ReadFile(store.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("rejected state was rewritten")
			}
		})
	}
}

func TestValidateSessionTitleInvariants(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		wantErr bool
	}{
		{name: "empty clears", title: ""},
		{name: "unicode", title: "Release 日本 build"},
		{name: "maximum", title: strings.Repeat("界", MaxSessionTitleRunes)},
		{name: "leading space", title: " release", wantErr: true},
		{name: "trailing space", title: "release ", wantErr: true},
		{name: "newline", title: "release\nnow", wantErr: true},
		{name: "too long", title: strings.Repeat("x", MaxSessionTitleRunes+1), wantErr: true},
		{name: "invalid utf8", title: string([]byte{0xff}), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateSessionTitle(test.title)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateSessionTitle(%q) error = %v, wantErr %t", test.title, err, test.wantErr)
			}
		})
	}
}

func storeFromFixture(t *testing.T, name string) FileStore {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	path := filepath.Join(base, "state.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return FileStore{Path: path, Artifacts: filepath.Join(base, "artifacts")}
}

func readPersistedState(t *testing.T, path string) State {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestRebaseArtifactsRepointsRelocatedDirectories(t *testing.T) {
	base := t.TempDir()
	previous := filepath.Join(base, "legacy", "workstreams")
	current := filepath.Join(base, ".heikou", "workstreams")
	store := FileStore{Path: filepath.Join(base, ".heikou", "state.json"), Artifacts: current}
	now := time.Unix(1_700_000_500, 0).UTC()

	inside := "018f0000-0000-4000-8000-0000000000a1"
	outside := "018f0000-0000-4000-8000-0000000000a2"
	external := filepath.Join(base, "elsewhere", outside)
	if _, err := store.Mutate(context.Background(), func(state *State) (bool, error) {
		state.Workstreams = append(state.Workstreams,
			Workstream{
				ID: inside, Name: "Relocated", ArtifactDir: filepath.Join(previous, inside),
				Roots: []string{base}, Revision: 3, CreatedAt: now, UpdatedAt: now,
			},
			Workstream{
				ID: outside, Name: "Elsewhere", ArtifactDir: external,
				Roots: []string{base}, Revision: 7, CreatedAt: now, UpdatedAt: now,
			},
		)
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}

	rebased, err := store.RebaseArtifacts(context.Background(), previous)
	if err != nil {
		t.Fatal(err)
	}
	if rebased != 1 {
		t.Fatalf("rebased %d workstreams, want 1", rebased)
	}

	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	relocated, ok := loaded.Workstream(inside)
	if !ok {
		t.Fatal("relocated workstream missing")
	}
	if want := filepath.Join(current, inside); relocated.ArtifactDir != want {
		t.Fatalf("ArtifactDir = %q, want %q", relocated.ArtifactDir, want)
	}
	// A relocation is not a domain edit, so the workstream's own revision and
	// timestamps must be untouched.
	if relocated.Revision != 3 || !relocated.UpdatedAt.Equal(now) {
		t.Fatalf("relocation altered revision/UpdatedAt: %d %v", relocated.Revision, relocated.UpdatedAt)
	}

	untouched, ok := loaded.Workstream(outside)
	if !ok {
		t.Fatal("external workstream missing")
	}
	if untouched.ArtifactDir != external {
		t.Fatalf("ArtifactDir outside the previous base was rewritten to %q", untouched.ArtifactDir)
	}

	again, err := store.RebaseArtifacts(context.Background(), previous)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("second rebase changed %d workstreams, want 0", again)
	}
}

func TestRebaseArtifactsIgnoresEmptyPreviousBase(t *testing.T) {
	base := t.TempDir()
	store := FileStore{Path: filepath.Join(base, "state.json"), Artifacts: filepath.Join(base, "data")}
	rebased, err := store.RebaseArtifacts(context.Background(), "  ")
	if err != nil {
		t.Fatal(err)
	}
	if rebased != 0 {
		t.Fatalf("rebased %d workstreams for an empty base, want 0", rebased)
	}
}
