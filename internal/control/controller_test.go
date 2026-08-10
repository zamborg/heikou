package control

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/workstream"
)

type memoryRepository struct {
	mu          sync.Mutex
	lifecycleMu sync.Mutex
	state       workstream.State
	path        string
	artifacts   string
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
func (r *memoryRepository) WithLifecycleLock(_ context.Context, operation func() error) error {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	return operation()
}

type fakeSupervisor struct {
	mu       sync.Mutex
	sessions []heikou.Session
	start    func(heikou.StartRequest) (heikou.Session, error)
	exists   func(string, string) (bool, error)
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
func (f *fakeSupervisor) RuntimeExists(_ context.Context, id, boundName string) (bool, error) {
	if f.exists != nil {
		return f.exists(id, boundName)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, session := range f.sessions {
		if session.ID == id || session.Name == boundName || session.Name == "h-"+id {
			return true, nil
		}
	}
	return false, nil
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
	prompt := "\tbuild it\n  exactly\n"
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
		if request.Prompt != prompt || record.InitialPrompt != prompt {
			t.Fatalf("prompt was not preserved: request=%q record=%q", request.Prompt, record.InitialPrompt)
		}
		runtime := heikou.Session{ID: request.ID, Name: "h-" + request.ID, PaneID: "%1", Backend: request.Backend, Prompt: request.Prompt, Root: request.Root, Status: heikou.StatusLive, StartedAt: controller.now()}
		supervisor.sessions = append(supervisor.sessions, runtime)
		return runtime, nil
	}
	session, err := controller.Start(context.Background(), StartRequest{Backend: heikou.BackendCodex, Prompt: prompt, Root: root, WorkstreamID: container.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !session.Alive() || session.Record.Launch.Binding == nil || session.Record.Launch.Binding.Socket != "heikou-test" {
		t.Fatalf("successful projection = %#v", session)
	}
	if session.Prompt != prompt || session.Record.InitialPrompt != prompt {
		t.Fatalf("projected prompt was not preserved: session=%q record=%q", session.Prompt, session.Record.InitialPrompt)
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

func TestDeleteSessionRefusesAnyRetainedRuntime(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	id := "018f0000-0000-4000-8000-000000000032"

	for _, test := range []struct {
		name   string
		status heikou.Status
	}{
		{name: "live pane", status: heikou.StatusLive},
		{name: "dead retained pane", status: heikou.StatusExited},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newMemoryRepository(root)
			controller := New(&fakeSupervisor{sessions: []heikou.Session{{
				ID: id, Name: "h-" + id, Backend: heikou.BackendCodex, Status: test.status,
			}}}, repository, "heikou-test")
			container, err := controller.CreateWorkstream(context.Background(), "Core", "", []string{root})
			if err != nil {
				t.Fatal(err)
			}
			_, err = repository.Mutate(context.Background(), func(state *workstream.State) (bool, error) {
				state.Sessions = append(state.Sessions, workstream.SessionRecord{
					ID: id, Backend: heikou.BackendCodex, InitialPrompt: "keep me", InitialRoot: root,
					CreatedAt: now, Launch: workstream.LaunchIntent{
						Status: workstream.LaunchStarted,
						Binding: &workstream.RuntimeBinding{
							Driver: "tmux", Socket: "heikou-test", SessionName: "h-" + id, BoundAt: now,
						},
					},
				})
				state.Memberships = append(state.Memberships, workstream.Membership{
					WorkstreamID: container.ID, SessionID: id, JoinedAt: now,
				})
				return true, nil
			})
			if err != nil {
				t.Fatal(err)
			}

			if err := controller.DeleteSession(context.Background(), id); err == nil {
				t.Fatal("DeleteSession removed a record with a retained runtime")
			}
			state, err := repository.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := state.Session(id); !ok || state.WorkstreamForSession(id) != container.ID {
				t.Fatalf("refused delete changed durable state: %#v", state)
			}
		})
	}
}

func TestDeleteSessionRemovesAbsentRuntimeRecordAndMembership(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	exitCode := 0

	for index, test := range []struct {
		name    string
		outcome *workstream.Outcome
	}{
		{name: "stopped", outcome: &workstream.Outcome{Kind: workstream.OutcomeStopped, RecordedAt: now}},
		{name: "start failed", outcome: &workstream.Outcome{Kind: workstream.OutcomeStartFailed, Error: "runner missing", RecordedAt: now}},
		{name: "exited", outcome: &workstream.Outcome{Kind: workstream.OutcomeExited, ExitCode: &exitCode, RecordedAt: now}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newMemoryRepository(root)
			controller := New(&fakeSupervisor{}, repository, "heikou-test")
			container, err := controller.CreateWorkstream(context.Background(), "Core", "", []string{root})
			if err != nil {
				t.Fatal(err)
			}
			id := "018f0000-0000-4000-8000-00000000004" + string(rune('1'+index))
			_, err = repository.Mutate(context.Background(), func(state *workstream.State) (bool, error) {
				state.Sessions = append(state.Sessions, workstream.SessionRecord{
					ID: id, Backend: heikou.BackendClaude, InitialPrompt: "delete me", InitialRoot: root,
					CreatedAt: now, Launch: workstream.LaunchIntent{Status: workstream.LaunchPending}, Outcome: test.outcome,
				})
				state.Memberships = append(state.Memberships, workstream.Membership{
					WorkstreamID: container.ID, SessionID: id, JoinedAt: now,
				})
				return true, nil
			})
			if err != nil {
				t.Fatal(err)
			}

			if err := controller.DeleteSession(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			state, err := repository.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := state.Session(id); ok {
				t.Fatalf("session %s remains after deletion", id)
			}
			for _, membership := range state.Memberships {
				if membership.SessionID == id {
					t.Fatalf("membership remains after deletion: %#v", membership)
				}
			}
		})
	}
}

func TestDeleteSessionRefusesUnboundPendingLaunchWithUnknownSocket(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	id := "018f0000-0000-4000-8000-000000000055"
	repository := newMemoryRepository(root)
	before, err := repository.Mutate(context.Background(), func(state *workstream.State) (bool, error) {
		state.Sessions = append(state.Sessions, workstream.SessionRecord{
			ID: id, Backend: heikou.BackendCodex, InitialPrompt: "ambiguous launch", InitialRoot: root,
			CreatedAt: now, Launch: workstream.LaunchIntent{Status: workstream.LaunchPending},
		})
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	supervisor := &fakeSupervisor{exists: func(_, _ string) (bool, error) {
		t.Fatal("an unbound pending launch must be rejected before checking only the current socket")
		return false, nil
	}}
	controller := New(supervisor, repository, "socket-b")
	err = controller.DeleteSession(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), "tmux socket is unknown") {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	after, loadErr := repository.Load(context.Background())
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, ok := after.Session(id); !ok {
		t.Fatal("ambiguous pending-launch delete removed the durable record")
	}
	if after.Revision != before.Revision {
		t.Fatalf("refused delete advanced revision from %d to %d", before.Revision, after.Revision)
	}
}

func TestDeleteSessionRejectsUnknownID(t *testing.T) {
	repository := newMemoryRepository(t.TempDir())
	controller := New(&fakeSupervisor{}, repository, "heikou-test")
	before, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	id := "018f0000-0000-4000-8000-000000000051"
	if err := controller.DeleteSession(context.Background(), id); err == nil {
		t.Fatal("DeleteSession accepted an unknown durable id")
	}
	after, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision {
		t.Fatalf("unknown delete changed revision from %d to %d", before.Revision, after.Revision)
	}
}

func TestDeleteSessionFailsClosedForPlausibleUnprojectableRuntime(t *testing.T) {
	root := t.TempDir()
	id := "018f0000-0000-4000-8000-000000000053"
	now := time.Now().UTC()
	repository := newMemoryRepository(root)
	_, err := repository.Mutate(context.Background(), func(state *workstream.State) (bool, error) {
		state.Sessions = append(state.Sessions, workstream.SessionRecord{
			ID: id, Backend: heikou.BackendCodex, InitialPrompt: "keep partial runtime", InitialRoot: root,
			CreatedAt: now, Launch: workstream.LaunchIntent{
				Status: workstream.LaunchStarted,
				Binding: &workstream.RuntimeBinding{
					Driver: "tmux", Socket: "heikou-test", SessionName: "h-" + id, BoundAt: now,
				},
			},
		})
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	supervisor := &fakeSupervisor{exists: func(gotID, gotName string) (bool, error) {
		if gotID != id || gotName != "h-"+id {
			t.Fatalf("RuntimeExists(%q, %q)", gotID, gotName)
		}
		return true, nil
	}}
	controller := New(supervisor, repository, "heikou-test")
	if err := controller.DeleteSession(context.Background(), id); err == nil || !strings.Contains(err.Error(), "runtime exists") {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	state, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Session(id); !ok {
		t.Fatal("fail-closed delete removed the durable record")
	}
}

func TestDeleteSessionRefusesDifferentBoundSocket(t *testing.T) {
	root := t.TempDir()
	id := "018f0000-0000-4000-8000-000000000054"
	now := time.Now().UTC()
	repository := newMemoryRepository(root)
	_, err := repository.Mutate(context.Background(), func(state *workstream.State) (bool, error) {
		state.Sessions = append(state.Sessions, workstream.SessionRecord{
			ID: id, Backend: heikou.BackendClaude, InitialPrompt: "bound elsewhere", InitialRoot: root,
			CreatedAt: now,
			Launch: workstream.LaunchIntent{Status: workstream.LaunchStarted, Binding: &workstream.RuntimeBinding{
				Driver: "tmux", Socket: "socket-a", SessionName: "h-" + id, BoundAt: now,
			}},
		})
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	supervisor := &fakeSupervisor{exists: func(_, _ string) (bool, error) {
		t.Fatal("a mismatched binding must be rejected before querying the current socket")
		return false, nil
	}}
	controller := New(supervisor, repository, "socket-b")
	err = controller.DeleteSession(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), `--socket "socket-a"`) {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	state, loadErr := repository.Load(context.Background())
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, ok := state.Session(id); !ok {
		t.Fatal("socket-mismatch delete removed the durable record")
	}
}

func TestDeleteSessionRejectsOrphanedRuntime(t *testing.T) {
	id := "018f0000-0000-4000-8000-000000000052"
	repository := newMemoryRepository(t.TempDir())
	supervisor := &fakeSupervisor{sessions: []heikou.Session{{
		ID: id, Name: "h-" + id, Backend: heikou.BackendCodex, Status: heikou.StatusLive,
	}}}
	controller := New(supervisor, repository, "heikou-test")
	if err := controller.DeleteSession(context.Background(), id); err == nil {
		t.Fatal("DeleteSession accepted an orphaned runtime")
	}
	state, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Sessions) != 0 || len(state.Memberships) != 0 {
		t.Fatalf("orphan rejection created durable state: %#v", state)
	}
	observed, err := supervisor.Find(context.Background(), id)
	if err != nil || observed.ID != id {
		t.Fatal("orphan rejection stopped the runtime")
	}
}

func TestDeleteSessionCannotRaceAConcurrentStartIntoAnOrphan(t *testing.T) {
	root := t.TempDir()
	repository := newMemoryRepository(root)
	supervisor := &fakeSupervisor{}
	starter := New(supervisor, repository, "heikou-test")
	deleter := New(supervisor, repository, "heikou-test")
	requestSeen := make(chan heikou.StartRequest, 1)
	releaseStart := make(chan struct{})
	supervisor.start = func(request heikou.StartRequest) (heikou.Session, error) {
		requestSeen <- request
		<-releaseStart
		runtime := heikou.Session{
			ID: request.ID, Name: "h-" + request.ID, PaneID: "%9", Backend: request.Backend,
			Prompt: request.Prompt, Root: request.Root, Status: heikou.StatusLive, StartedAt: time.Now().UTC(),
		}
		supervisor.mu.Lock()
		supervisor.sessions = append(supervisor.sessions, runtime)
		supervisor.mu.Unlock()
		return runtime, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type startResult struct {
		session Session
		err     error
	}
	started := make(chan startResult, 1)
	go func() {
		session, err := starter.Start(ctx, StartRequest{Backend: heikou.BackendCodex, Prompt: "launch", Root: root})
		started <- startResult{session: session, err: err}
	}()
	request := <-requestSeen

	deleted := make(chan error, 1)
	go func() { deleted <- deleter.DeleteSession(ctx, request.ID) }()
	select {
	case err := <-deleted:
		t.Fatalf("delete completed while start held the lifecycle boundary: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseStart)
	result := <-started
	if result.err != nil || !result.session.Alive() {
		t.Fatalf("Start() = session %#v, error %v", result.session, result.err)
	}
	if err := <-deleted; err == nil || !strings.Contains(err.Error(), "runtime exists") {
		t.Fatalf("DeleteSession() after concurrent start error = %v", err)
	}
	state, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Session(request.ID); !ok {
		t.Fatal("concurrent delete removed the launched session record")
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
