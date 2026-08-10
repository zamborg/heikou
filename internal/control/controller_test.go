package control

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/workstream"
)

type memoryRepository struct {
	mu        sync.Mutex
	state     workstream.State
	path      string
	artifacts string
}

func newMemoryRepository(root string) *memoryRepository {
	return &memoryRepository{state: workstream.EmptyState(), path: filepath.Join(root, "state.json"), artifacts: filepath.Join(root, "artifacts")}
}

func (r *memoryRepository) Load(context.Context) (workstream.State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneState(r.state), nil
}

func (r *memoryRepository) Mutate(_ context.Context, mutate func(*workstream.State) (bool, error)) (workstream.State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := cloneState(r.state)
	changed, err := mutate(&state)
	if err != nil {
		return workstream.State{}, err
	}
	if changed {
		state.Version = workstream.StateVersion
		state.Revision++
		if err := state.Validate(); err != nil {
			return workstream.State{}, err
		}
		r.state = cloneState(state)
	}
	return cloneState(state), nil
}

func (r *memoryRepository) StatePath() string    { return r.path }
func (r *memoryRepository) ArtifactBase() string { return r.artifacts }

type fakeSupervisor struct {
	mu       sync.Mutex
	sessions []heikou.Session
	start    func(heikou.StartRequest) (heikou.Session, error)
	stopErr  error
}

func (f *fakeSupervisor) Bootstrap(context.Context) error { return nil }
func (f *fakeSupervisor) Sessions(context.Context) ([]heikou.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]heikou.Session(nil), f.sessions...), nil
}
func (f *fakeSupervisor) Find(_ context.Context, query string) (heikou.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, session := range f.sessions {
		if session.ID == query {
			return session, nil
		}
	}
	return heikou.Session{}, errors.New("not found")
}
func (f *fakeSupervisor) Start(_ context.Context, request heikou.StartRequest) (heikou.Session, error) {
	if f.start != nil {
		return f.start(request)
	}
	return heikou.Session{}, errors.New("start failed")
}
func (f *fakeSupervisor) Send(context.Context, heikou.Session, string) error { return nil }
func (f *fakeSupervisor) Capture(context.Context, heikou.Session, int) (string, error) {
	return "", nil
}
func (f *fakeSupervisor) Stop(_ context.Context, session heikou.Session) error {
	if f.stopErr != nil {
		return f.stopErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	remaining := f.sessions[:0]
	for _, candidate := range f.sessions {
		if candidate.ID != session.ID {
			remaining = append(remaining, candidate)
		}
	}
	f.sessions = remaining
	return nil
}
func (f *fakeSupervisor) AttachCommand(heikou.Session) *exec.Cmd { return exec.Command("true") }

func TestStartPersistsPendingIdentityBeforeSupervisorAndBindsSuccess(t *testing.T) {
	root := t.TempDir()
	repository := newMemoryRepository(root)
	supervisor := &fakeSupervisor{}
	controller := New(supervisor, repository, "heikou-test")
	controller.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	container, err := controller.CreateWorkstream(context.Background(), "Core", "", []string{root})
	if err != nil {
		t.Fatal(err)
	}
	supervisor.start = func(request heikou.StartRequest) (heikou.Session, error) {
		state, loadErr := repository.Load(context.Background())
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		record, ok := state.Session(request.ID)
		if !ok || record.Launch.Status != workstream.LaunchPending {
			t.Fatalf("pending record did not precede Start: %#v", state)
		}
		if got := state.WorkstreamForSession(request.ID); got != container.ID {
			t.Fatalf("membership = %q, want %q", got, container.ID)
		}
		runtime := heikou.Session{ID: request.ID, Name: "h-" + request.ID, PaneID: "%1", Backend: request.Backend, Prompt: request.Prompt, Root: request.Root, Status: heikou.StatusLive, StartedAt: controller.now()}
		supervisor.sessions = append(supervisor.sessions, runtime)
		return runtime, nil
	}
	session, err := controller.Start(context.Background(), StartRequest{Backend: heikou.BackendCodex, Prompt: "build it", Root: root, WorkstreamID: container.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !session.Alive() || session.Record.Launch.Binding == nil || session.Record.Launch.Binding.Socket != "heikou-test" {
		t.Fatalf("successful projection = %#v", session)
	}
}

func TestFailedStartKeepsRecordMembershipAndOutcome(t *testing.T) {
	root := t.TempDir()
	repository := newMemoryRepository(root)
	controller := New(&fakeSupervisor{}, repository, "heikou-test")
	container, err := controller.CreateWorkstream(context.Background(), "Core", "", []string{root})
	if err != nil {
		t.Fatal(err)
	}
	session, err := controller.Start(context.Background(), StartRequest{Backend: heikou.BackendClaude, Prompt: "fail safely", Root: root, WorkstreamID: container.ID})
	if err == nil {
		t.Fatal("Start succeeded")
	}
	state, loadErr := repository.Load(context.Background())
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	record, ok := state.Session(session.ID)
	if !ok || record.Outcome == nil || record.Outcome.Kind != workstream.OutcomeStartFailed {
		t.Fatalf("failed durable record = %#v", record)
	}
	if state.WorkstreamForSession(session.ID) != container.ID {
		t.Fatal("failed launch membership was rolled back")
	}
}

func TestReconciliationIsConservativeAndFindsOrphans(t *testing.T) {
	root := t.TempDir()
	repository := newMemoryRepository(root)
	now := time.Unix(1_700_000_100, 0).UTC()
	ids := []string{
		"018f0000-0000-4000-8000-000000000021",
		"018f0000-0000-4000-8000-000000000022",
		"018f0000-0000-4000-8000-000000000023",
		"018f0000-0000-4000-8000-000000000024",
	}
	_, err := repository.Mutate(context.Background(), func(state *workstream.State) (bool, error) {
		for _, id := range ids[:3] {
			state.Sessions = append(state.Sessions, workstream.SessionRecord{ID: id, Backend: heikou.BackendCodex, InitialPrompt: id, InitialRoot: root, CreatedAt: now.Add(-time.Minute), Launch: workstream.LaunchIntent{Status: workstream.LaunchPending}})
		}
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	supervisor := &fakeSupervisor{sessions: []heikou.Session{
		{ID: ids[0], Name: "h-" + ids[0], Backend: heikou.BackendCodex, Status: heikou.StatusLive, StartedAt: now.Add(-time.Minute)},
		{ID: ids[1], Name: "h-" + ids[1], Backend: heikou.BackendCodex, Status: heikou.StatusFailed, ExitCode: 7, StartedAt: now.Add(-time.Minute), EndedAt: now},
		{ID: ids[3], Name: "h-" + ids[3], Backend: heikou.BackendClaude, Status: heikou.StatusLive, StartedAt: now},
	}}
	controller := New(supervisor, repository, "heikou-test")
	controller.now = func() time.Time { return now }
	snapshot, err := controller.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	statuses := make(map[string]Status)
	for _, session := range snapshot.Sessions {
		statuses[session.ID] = session.Status
	}
	if statuses[ids[0]] != StatusLive || statuses[ids[1]] != StatusExited || statuses[ids[2]] != StatusUnavailable {
		t.Fatalf("reconciled statuses = %#v", statuses)
	}
	if len(snapshot.Orphans) != 1 || snapshot.Orphans[0].ID != ids[3] {
		t.Fatalf("orphans = %#v", snapshot.Orphans)
	}
	state, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dead, _ := state.Session(ids[1])
	if dead.Outcome == nil || dead.Outcome.Kind != workstream.OutcomeExited || dead.Outcome.ExitCode == nil || *dead.Outcome.ExitCode != 7 {
		t.Fatalf("dead outcome = %#v", dead.Outcome)
	}
	unavailable, _ := state.Session(ids[2])
	if unavailable.Outcome != nil {
		t.Fatalf("absence inferred an outcome: %#v", unavailable.Outcome)
	}
	revision := state.Revision
	if _, err := controller.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, _ = repository.Load(context.Background())
	if state.Revision != revision {
		t.Fatalf("idempotent reconciliation advanced revision from %d to %d", revision, state.Revision)
	}
}

func TestStopRecordsOutcomeOnlyAfterTmuxKillSucceeds(t *testing.T) {
	root := t.TempDir()
	repository := newMemoryRepository(root)
	id := "018f0000-0000-4000-8000-000000000031"
	now := time.Now()
	_, err := repository.Mutate(context.Background(), func(state *workstream.State) (bool, error) {
		state.Sessions = append(state.Sessions, workstream.SessionRecord{ID: id, Backend: heikou.BackendCodex, InitialPrompt: "stop me", InitialRoot: root, CreatedAt: now, Launch: workstream.LaunchIntent{Status: workstream.LaunchPending}})
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := heikou.Session{ID: id, Name: "h-" + id, PaneID: "%1", Backend: heikou.BackendCodex, Status: heikou.StatusLive, StartedAt: now}
	supervisor := &fakeSupervisor{sessions: []heikou.Session{runtime}, stopErr: errors.New("kill failed")}
	controller := New(supervisor, repository, "heikou-test")
	if err := controller.Stop(context.Background(), id); err == nil {
		t.Fatal("failed kill was reported as success")
	}
	state, _ := repository.Load(context.Background())
	record, _ := state.Session(id)
	if record.Outcome != nil {
		t.Fatalf("failed kill recorded an outcome: %#v", record.Outcome)
	}
	supervisor.stopErr = nil
	if err := controller.Stop(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	state, _ = repository.Load(context.Background())
	record, _ = state.Session(id)
	if record.Outcome == nil || record.Outcome.Kind != workstream.OutcomeStopped {
		t.Fatalf("successful kill outcome = %#v", record.Outcome)
	}
}

func TestPositiveRuntimeEvidenceWinsOverAmbiguousStartError(t *testing.T) {
	root := t.TempDir()
	repository := newMemoryRepository(root)
	supervisor := &fakeSupervisor{}
	controller := New(supervisor, repository, "heikou-test")
	supervisor.start = func(request heikou.StartRequest) (heikou.Session, error) {
		runtime := heikou.Session{ID: request.ID, Name: "h-" + request.ID, PaneID: "%1", Backend: request.Backend, Prompt: request.Prompt, Root: request.Root, Status: heikou.StatusLive, StartedAt: time.Now()}
		supervisor.sessions = append(supervisor.sessions, runtime)
		return heikou.Session{}, context.DeadlineExceeded
	}
	session, err := controller.Start(context.Background(), StartRequest{Backend: heikou.BackendNoAgent, Prompt: "ambiguous", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if !session.Alive() || session.Record.Outcome != nil || session.Record.Launch.Status != workstream.LaunchStarted {
		t.Fatalf("ambiguous start projection = %#v", session)
	}
}

func TestOrphanRequiresExplicitAdoptionBeforeMembership(t *testing.T) {
	root := t.TempDir()
	repository := newMemoryRepository(root)
	id := "018f0000-0000-4000-8000-000000000041"
	now := time.Now()
	runtime := heikou.Session{ID: id, Name: "h-" + id, PaneID: "%1", Backend: heikou.BackendClaude, Prompt: "legacy task", Root: root, Status: heikou.StatusLive, StartedAt: now}
	supervisor := &fakeSupervisor{sessions: []heikou.Session{runtime}}
	controller := New(supervisor, repository, "heikou-test")
	container, err := controller.CreateWorkstream(context.Background(), "Imported", "", []string{root})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := controller.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Orphans) != 1 || len(snapshot.Sessions) != 0 {
		t.Fatalf("pre-adoption snapshot = %#v", snapshot)
	}
	state, _ := repository.Load(context.Background())
	if len(state.Sessions) != 0 || len(state.Memberships) != 0 {
		t.Fatal("reconciliation silently adopted an orphan")
	}
	adopted, err := controller.AdoptSession(context.Background(), id, container.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !adopted.Durable || adopted.Orphaned || adopted.WorkstreamID != container.ID || !adopted.Alive() {
		t.Fatalf("adopted projection = %#v", adopted)
	}
	snapshot, err = controller.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Orphans) != 0 || len(snapshot.Sessions) != 1 {
		t.Fatalf("post-adoption snapshot = %#v", snapshot)
	}
}

func cloneState(state workstream.State) workstream.State {
	copy := state
	copy.Workstreams = append([]workstream.Workstream(nil), state.Workstreams...)
	for index := range copy.Workstreams {
		copy.Workstreams[index].Roots = append([]string(nil), copy.Workstreams[index].Roots...)
	}
	copy.Sessions = append([]workstream.SessionRecord(nil), state.Sessions...)
	for index := range copy.Sessions {
		if state.Sessions[index].Launch.Binding != nil {
			binding := *state.Sessions[index].Launch.Binding
			copy.Sessions[index].Launch.Binding = &binding
		}
		if state.Sessions[index].Outcome != nil {
			outcome := *state.Sessions[index].Outcome
			if outcome.ExitCode != nil {
				code := *outcome.ExitCode
				outcome.ExitCode = &code
			}
			copy.Sessions[index].Outcome = &outcome
		}
	}
	copy.Memberships = append([]workstream.Membership(nil), state.Memberships...)
	return copy
}
