package control

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/workstream"
)

func TestReplaceRootPreservesOrderAndHistoricalInitialRoot(t *testing.T) {
	base := t.TempDir()
	roots := rootMutationDirectories(t, base, "first", "historical", "third", "replacement")
	repository := newMemoryRepository(base)
	controller := New(&fakeSupervisor{}, repository, "heikou-test")
	createdAt := time.Unix(1_700_000_000, 0).UTC()
	controller.now = func() time.Time { return createdAt }
	container := rootMutationWorkstream(t, controller, roots[:3])

	const sessionID = "018f0000-0000-4000-8000-000000000061"
	_, err := repository.Mutate(context.Background(), func(state *workstream.State) (bool, error) {
		state.Sessions = append(state.Sessions, workstream.SessionRecord{
			ID: sessionID, Backend: heikou.BackendCodex, InitialPrompt: "historical launch",
			InitialRoot: roots[1], CreatedAt: createdAt,
			Launch: workstream.LaunchIntent{Status: workstream.LaunchPending},
		})
		state.Memberships = append(state.Memberships, workstream.Membership{
			WorkstreamID: container.ID, SessionID: sessionID, JoinedAt: createdAt,
		})
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before := rootMutationState(t, repository)
	beforeContainer, _ := before.Workstream(container.ID)

	updatedAt := createdAt.Add(time.Hour)
	controller.now = func() time.Time { return updatedAt }
	if err := controller.ReplaceRoot(context.Background(), container.ID, roots[1], roots[3]); err != nil {
		t.Fatal(err)
	}

	after := rootMutationState(t, repository)
	afterContainer, ok := after.Workstream(container.ID)
	if !ok {
		t.Fatalf("workstream %s disappeared", container.ID)
	}
	if want := []string{roots[0], roots[3], roots[2]}; !slices.Equal(afterContainer.Roots, want) {
		t.Fatalf("roots = %#v, want %#v", afterContainer.Roots, want)
	}
	if afterContainer.Revision != beforeContainer.Revision+1 || after.Revision != before.Revision+1 {
		t.Fatalf("revisions = workstream %d, state %d; want %d and %d",
			afterContainer.Revision, after.Revision, beforeContainer.Revision+1, before.Revision+1)
	}
	if !afterContainer.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("updated_at = %v, want %v", afterContainer.UpdatedAt, updatedAt)
	}
	record, ok := after.Session(sessionID)
	if !ok {
		t.Fatalf("historical session %s disappeared", sessionID)
	}
	if record.InitialRoot != roots[1] {
		t.Fatalf("historical initial_root = %q, want %q", record.InitialRoot, roots[1])
	}
}

func TestReplaceRootSameNormalizedRootIsNoOp(t *testing.T) {
	base := t.TempDir()
	root := rootMutationDirectories(t, base, "root")[0]
	repository := newMemoryRepository(base)
	controller := New(&fakeSupervisor{}, repository, "heikou-test")
	container := rootMutationWorkstream(t, controller, []string{root})
	before := rootMutationState(t, repository)

	current := root + string(filepath.Separator) + "."
	replacement := root + string(filepath.Separator) + "not-created" + string(filepath.Separator) + ".."
	if err := controller.ReplaceRoot(context.Background(), container.ID, current, replacement); err != nil {
		t.Fatal(err)
	}
	after := rootMutationState(t, repository)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("same normalized root changed state:\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestReplaceRootRejectsDuplicateReplacement(t *testing.T) {
	base := t.TempDir()
	roots := rootMutationDirectories(t, base, "first", "second")
	repository := newMemoryRepository(base)
	controller := New(&fakeSupervisor{}, repository, "heikou-test")
	container := rootMutationWorkstream(t, controller, roots)
	before := rootMutationState(t, repository)

	err := controller.ReplaceRoot(context.Background(), container.ID, roots[0], roots[1])
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("ReplaceRoot duplicate error = %v", err)
	}
	after := rootMutationState(t, repository)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected duplicate changed state:\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestRemoveRootRemovesOneOfMany(t *testing.T) {
	base := t.TempDir()
	roots := rootMutationDirectories(t, base, "first", "remove", "third")
	repository := newMemoryRepository(base)
	controller := New(&fakeSupervisor{}, repository, "heikou-test")
	container := rootMutationWorkstream(t, controller, roots)
	before := rootMutationState(t, repository)
	beforeContainer, _ := before.Workstream(container.ID)

	if err := controller.RemoveRoot(context.Background(), container.ID, roots[1]); err != nil {
		t.Fatal(err)
	}
	after := rootMutationState(t, repository)
	afterContainer, ok := after.Workstream(container.ID)
	if !ok {
		t.Fatalf("workstream %s disappeared", container.ID)
	}
	if want := []string{roots[0], roots[2]}; !slices.Equal(afterContainer.Roots, want) {
		t.Fatalf("roots = %#v, want %#v", afterContainer.Roots, want)
	}
	if afterContainer.Revision != beforeContainer.Revision+1 || after.Revision != before.Revision+1 {
		t.Fatalf("revisions = workstream %d, state %d; want %d and %d",
			afterContainer.Revision, after.Revision, beforeContainer.Revision+1, before.Revision+1)
	}
}

func TestRemoveRootRejectsFinalRootWithoutRevisionChange(t *testing.T) {
	base := t.TempDir()
	root := rootMutationDirectories(t, base, "only")[0]
	repository := newMemoryRepository(base)
	controller := New(&fakeSupervisor{}, repository, "heikou-test")
	container := rootMutationWorkstream(t, controller, []string{root})
	before := rootMutationState(t, repository)

	err := controller.RemoveRoot(context.Background(), container.ID, root)
	if err == nil || !strings.Contains(err.Error(), "at least one root") {
		t.Fatalf("RemoveRoot final-root error = %v", err)
	}
	after := rootMutationState(t, repository)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected final-root removal changed state:\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestRootMutationRejectsMissingCurrentRoot(t *testing.T) {
	base := t.TempDir()
	roots := rootMutationDirectories(t, base, "registered", "replacement")
	repository := newMemoryRepository(base)
	controller := New(&fakeSupervisor{}, repository, "heikou-test")
	container := rootMutationWorkstream(t, controller, roots[:1])
	missing := filepath.Join(base, "missing")
	before := rootMutationState(t, repository)

	if err := controller.ReplaceRoot(context.Background(), container.ID, missing, roots[1]); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("ReplaceRoot missing-current error = %v", err)
	}
	if err := controller.RemoveRoot(context.Background(), container.ID, missing); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("RemoveRoot missing-current error = %v", err)
	}
	after := rootMutationState(t, repository)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("missing-current mutations changed state:\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func rootMutationDirectories(t *testing.T, base string, names ...string) []string {
	t.Helper()
	roots := make([]string, 0, len(names))
	for _, name := range names {
		root := filepath.Join(base, name)
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		roots = append(roots, root)
	}
	return roots
}

func rootMutationWorkstream(t *testing.T, controller *Controller, roots []string) workstream.Workstream {
	t.Helper()
	container, err := controller.CreateWorkstream(context.Background(), "Root mutations", "", roots)
	if err != nil {
		t.Fatal(err)
	}
	return container
}

func rootMutationState(t *testing.T, repository *memoryRepository) workstream.State {
	t.Helper()
	state, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return state
}
