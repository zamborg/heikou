package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/workstream"
)

type Status string

const (
	StatusLive        Status = "live"
	StatusExited      Status = "exited"
	StatusStopped     Status = "stopped"
	StatusStartFailed Status = "start_failed"
	StatusUnavailable Status = "unavailable"
)

type Session struct {
	ID      string
	Backend heikou.Backend
	Prompt  string
	// LastUserMessage is the latest bounded preview observed from the tmux
	// runtime. It covers sends through Heikou, not typing in an attached TUI.
	LastUserMessage string
	Root            string
	CreatedAt       time.Time
	WorkstreamID    string
	Status          Status
	Durable         bool
	Orphaned        bool
	Record          workstream.SessionRecord
	Runtime         *heikou.Session
}

// DisplayMessage returns the most recent user message Heikou can honestly
// observe, falling back to the immutable launch prompt.
func (s Session) DisplayMessage() string {
	if strings.TrimSpace(s.LastUserMessage) != "" {
		return s.LastUserMessage
	}
	return s.Prompt
}

func (s Session) Alive() bool {
	return s.Runtime != nil && s.Runtime.Alive()
}

func (s Session) Available() bool { return s.Runtime != nil }

func (s Session) ExitCode() (int, bool) {
	if s.Runtime != nil && !s.Runtime.Alive() {
		return s.Runtime.ExitCode, true
	}
	if s.Durable && s.Record.Outcome != nil && s.Record.Outcome.ExitCode != nil {
		return *s.Record.Outcome.ExitCode, true
	}
	return 0, false
}

func (s Session) RuntimeDuration(now time.Time) time.Duration {
	if s.Runtime != nil {
		return s.Runtime.Runtime(now)
	}
	end := now
	if s.Durable && s.Record.Outcome != nil {
		end = s.Record.Outcome.RecordedAt
	}
	if s.CreatedAt.IsZero() || end.Before(s.CreatedAt) {
		return 0
	}
	return end.Sub(s.CreatedAt)
}

func (s Session) LastActivity() time.Time {
	if s.Runtime != nil && !s.Runtime.LastActivityAt.IsZero() {
		return s.Runtime.LastActivityAt
	}
	if s.Durable && s.Record.Outcome != nil {
		return s.Record.Outcome.RecordedAt
	}
	return s.CreatedAt
}

type Snapshot struct {
	Revision    uint64
	Workstreams []workstream.Workstream
	Sessions    []Session
	Orphans     []Session
	StatePath   string
}

type StartRequest struct {
	Backend      heikou.Backend
	Prompt       string
	Root         string
	Command      []string
	WorkstreamID string
}

type Service interface {
	Snapshot(context.Context) (Snapshot, error)
	Find(context.Context, string) (Session, error)
	Start(context.Context, StartRequest) (Session, error)
	Send(context.Context, string, string) error
	Capture(context.Context, string, int) (string, error)
	Stop(context.Context, string) error
	DeleteSession(context.Context, string) error
	AttachCommand(context.Context, string) (*exec.Cmd, error)
	CreateWorkstream(context.Context, string, string, []string) (workstream.Workstream, error)
	RenameWorkstream(context.Context, string, string) error
	ArchiveWorkstream(context.Context, string) error
	MoveSession(context.Context, string, string) error
	AdoptSession(context.Context, string, string) (Session, error)
	AddRoot(context.Context, string, string) error
	ReplaceRoot(context.Context, string, string, string) error
	RemoveRoot(context.Context, string, string) error
}

type Controller struct {
	supervisor heikou.Supervisor
	store      workstream.Repository
	socket     string
	now        func() time.Time
}

func New(supervisor heikou.Supervisor, store workstream.Repository, socket string) *Controller {
	return &Controller{supervisor: supervisor, store: store, socket: socket, now: time.Now}
}

func (c *Controller) Snapshot(ctx context.Context) (Snapshot, error) {
	runtimes, err := c.supervisor.Sessions(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	runtimeByID := make(map[string]heikou.Session, len(runtimes))
	for _, runtime := range runtimes {
		runtimeByID[runtime.ID] = runtime
	}
	now := c.now()
	state, err := c.store.Mutate(ctx, func(state *workstream.State) (bool, error) {
		changed := false
		for index := range state.Sessions {
			record := &state.Sessions[index]
			runtime, ok := runtimeByID[record.ID]
			if !ok {
				continue
			}
			binding := runtimeBinding(c.socket, runtime, now)
			if record.Launch.Status != workstream.LaunchStarted || !sameBinding(record.Launch.Binding, binding) {
				record.Launch.Status = workstream.LaunchStarted
				record.Launch.Binding = &binding
				changed = true
			}
			if runtime.Alive() {
				if record.Outcome != nil && record.Outcome.Kind == workstream.OutcomeStartFailed {
					record.Outcome = nil
					changed = true
				}
				continue
			}
			if record.Outcome == nil || record.Outcome.Kind == workstream.OutcomeStartFailed {
				exitCode := runtime.ExitCode
				recordedAt := runtime.EndedAt
				if recordedAt.IsZero() {
					recordedAt = now
				}
				record.Outcome = &workstream.Outcome{
					Kind: workstream.OutcomeExited, ExitCode: &exitCode, RecordedAt: recordedAt,
				}
				changed = true
			}
		}
		return changed, nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	return project(state, runtimes, c.store.StatePath()), nil
}

func (c *Controller) Find(ctx context.Context, query string) (Session, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Session{}, errors.New("session id is required")
	}
	snapshot, err := c.Snapshot(ctx)
	if err != nil {
		return Session{}, err
	}
	all := append(append([]Session(nil), snapshot.Sessions...), snapshot.Orphans...)
	var matches []Session
	for _, session := range all {
		name := ""
		if session.Runtime != nil {
			name = session.Runtime.Name
		}
		if session.ID == query || name == query {
			return session, nil
		}
		if strings.HasPrefix(session.ID, query) || (name != "" && strings.HasPrefix(name, query)) {
			matches = append(matches, session)
		}
	}
	if len(matches) == 0 {
		return Session{}, fmt.Errorf("no session matches %q", query)
	}
	if len(matches) > 1 {
		return Session{}, fmt.Errorf("session prefix %q is ambiguous", query)
	}
	return matches[0], nil
}

func (c *Controller) Start(ctx context.Context, request StartRequest) (Session, error) {
	var session Session
	var startErr error
	if err := c.store.WithLifecycleLock(ctx, func() error {
		session, startErr = c.startLocked(ctx, request)
		return nil
	}); err != nil {
		return Session{}, err
	}
	return session, startErr
}

func (c *Controller) startLocked(ctx context.Context, request StartRequest) (Session, error) {
	prompt := strings.TrimSpace(request.Prompt)
	if prompt == "" {
		return Session{}, errors.New("prompt cannot be empty")
	}
	if _, err := heikou.ParseBackend(string(request.Backend)); err != nil {
		return Session{}, err
	}
	root, err := validateRoot(request.Root)
	if err != nil {
		return Session{}, err
	}
	id, err := workstream.NewID()
	if err != nil {
		return Session{}, err
	}
	now := c.now()
	record := workstream.SessionRecord{
		ID: id, Backend: request.Backend, InitialPrompt: prompt, InitialRoot: root,
		CreatedAt: now, Launch: workstream.LaunchIntent{Status: workstream.LaunchPending},
	}
	state, err := c.store.Mutate(ctx, func(state *workstream.State) (bool, error) {
		if _, exists := state.Session(id); exists {
			return false, fmt.Errorf("session id %s already exists", id)
		}
		if request.WorkstreamID != "" {
			container, ok := state.Workstream(request.WorkstreamID)
			if !ok || container.ArchivedAt != nil {
				return false, fmt.Errorf("workstream %q is not active", request.WorkstreamID)
			}
			if !containsRoot(container.Roots, root) {
				return false, fmt.Errorf("root %q is not registered in workstream %q", root, container.Name)
			}
		}
		state.Sessions = append(state.Sessions, record)
		if request.WorkstreamID != "" {
			state.Memberships = append(state.Memberships, workstream.Membership{
				WorkstreamID: request.WorkstreamID, SessionID: id, JoinedAt: now,
			})
		}
		return true, nil
	})
	if err != nil {
		return Session{}, err
	}

	runtime, launchErr := c.supervisor.Start(ctx, heikou.StartRequest{
		ID: id, Backend: request.Backend, Prompt: prompt, Root: root, Command: request.Command,
	})
	if launchErr != nil {
		// A cancelled tmux command can report an error after creating the pane.
		// Positive runtime evidence wins over that ambiguous error.
		recoveryCtx, recoveryCancel := context.WithTimeout(context.Background(), 2*time.Second)
		observed, findErr := c.supervisor.Find(recoveryCtx, id)
		recoveryCancel()
		if findErr == nil {
			runtime, launchErr = observed, nil
		}
	}
	if launchErr != nil {
		recordedAt := c.now()
		persistCtx, persistCancel := context.WithTimeout(context.Background(), 5*time.Second)
		state, persistErr := c.store.Mutate(persistCtx, func(state *workstream.State) (bool, error) {
			for index := range state.Sessions {
				if state.Sessions[index].ID == id {
					state.Sessions[index].Outcome = &workstream.Outcome{
						Kind: workstream.OutcomeStartFailed, Error: launchErr.Error(), RecordedAt: recordedAt,
					}
					return true, nil
				}
			}
			return false, fmt.Errorf("pending session %s disappeared", id)
		})
		persistCancel()
		view := projectOne(state, record, nil)
		if persistErr != nil {
			return view, fmt.Errorf("%v; record start failure: %w", launchErr, persistErr)
		}
		if current, ok := state.Session(id); ok {
			view = projectOne(state, current, nil)
		}
		return view, launchErr
	}

	boundAt := c.now()
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 5*time.Second)
	state, err = c.store.Mutate(persistCtx, func(state *workstream.State) (bool, error) {
		for index := range state.Sessions {
			if state.Sessions[index].ID != id {
				continue
			}
			binding := runtimeBinding(c.socket, runtime, boundAt)
			state.Sessions[index].Launch = workstream.LaunchIntent{Status: workstream.LaunchStarted, Binding: &binding}
			if state.Sessions[index].Outcome != nil && state.Sessions[index].Outcome.Kind == workstream.OutcomeStartFailed {
				state.Sessions[index].Outcome = nil
			}
			return true, nil
		}
		return false, fmt.Errorf("pending session %s disappeared", id)
	})
	persistCancel()
	view := projectOne(state, record, &runtime)
	if current, ok := state.Session(id); ok {
		view = projectOne(state, current, &runtime)
	}
	if err != nil {
		return view, fmt.Errorf("session started but runtime binding was not recorded: %w", err)
	}
	return view, nil
}

func (c *Controller) Send(ctx context.Context, id, message string) error {
	runtime, err := c.supervisor.Find(ctx, id)
	if err != nil {
		return err
	}
	return c.supervisor.Send(ctx, runtime, message)
}

func (c *Controller) Capture(ctx context.Context, id string, lines int) (string, error) {
	runtime, err := c.supervisor.Find(ctx, id)
	if err != nil {
		return "", err
	}
	return c.supervisor.Capture(ctx, runtime, lines)
}

func (c *Controller) Stop(ctx context.Context, id string) error {
	runtime, err := c.supervisor.Find(ctx, id)
	if err != nil {
		return err
	}
	if err := c.supervisor.Stop(ctx, runtime); err != nil {
		return err
	}
	now := c.now()
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err = c.store.Mutate(persistCtx, func(state *workstream.State) (bool, error) {
		for index := range state.Sessions {
			if state.Sessions[index].ID == id {
				state.Sessions[index].Outcome = &workstream.Outcome{Kind: workstream.OutcomeStopped, RecordedAt: now}
				return true, nil
			}
		}
		// Unknown IDs are orphaned tmux sessions, so there is no durable record
		// to change after a successful kill.
		return false, nil
	})
	persistCancel()
	if err != nil {
		return fmt.Errorf("runtime stopped but durable outcome was not recorded: %w", err)
	}
	return nil
}

// DeleteSession removes durable organization only. It deliberately refuses to
// remove a record while tmux still has a matching pane, even when that pane is
// dead, and never stops a runtime as a side effect.
func (c *Controller) DeleteSession(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("session id is required")
	}
	return c.store.WithLifecycleLock(ctx, func() error {
		return c.deleteSessionLocked(ctx, id)
	})
}

func (c *Controller) deleteSessionLocked(ctx context.Context, id string) error {
	state, err := c.store.Load(ctx)
	if err != nil {
		return err
	}
	record, ok := state.Session(id)
	if !ok {
		return fmt.Errorf("durable session %q not found", id)
	}
	boundName, err := c.deleteRuntimeName(record)
	if err != nil {
		return err
	}
	exists, err := c.supervisor.RuntimeExists(ctx, id, boundName)
	if err != nil {
		return fmt.Errorf("check runtime before deleting session %q: %w", id, err)
	}
	if exists {
		return fmt.Errorf("cannot delete session %q while its tmux runtime exists", id)
	}

	_, err = c.store.Mutate(ctx, func(state *workstream.State) (bool, error) {
		index := -1
		for candidate := range state.Sessions {
			if state.Sessions[candidate].ID == id {
				index = candidate
				break
			}
		}
		if index == -1 {
			return false, fmt.Errorf("durable session %q not found", id)
		}
		if _, err := c.deleteRuntimeName(state.Sessions[index]); err != nil {
			return false, err
		}

		state.Sessions = append(state.Sessions[:index], state.Sessions[index+1:]...)
		memberships := state.Memberships[:0]
		for _, membership := range state.Memberships {
			if membership.SessionID != id {
				memberships = append(memberships, membership)
			}
		}
		state.Memberships = memberships
		return true, nil
	})
	return err
}

func (c *Controller) deleteRuntimeName(record workstream.SessionRecord) (string, error) {
	if record.Launch.Binding == nil {
		if record.Launch.Status == workstream.LaunchPending && record.Outcome == nil {
			return "", fmt.Errorf(
				"cannot safely delete session %q: its pending launch has no runtime binding or terminal outcome, so the tmux socket is unknown",
				record.ID,
			)
		}
		return "h-" + record.ID, nil
	}
	binding := record.Launch.Binding
	if binding.Socket != c.socket {
		return "", fmt.Errorf(
			"cannot delete session %q from tmux socket %q; it is bound to socket %q (retry with --socket %q)",
			record.ID, c.socket, binding.Socket, binding.Socket,
		)
	}
	return binding.SessionName, nil
}

func (c *Controller) AttachCommand(ctx context.Context, id string) (*exec.Cmd, error) {
	runtime, err := c.supervisor.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	return c.supervisor.AttachCommand(runtime), nil
}

func (c *Controller) CreateWorkstream(ctx context.Context, name, description string, roots []string) (workstream.Workstream, error) {
	name = cleanName(name)
	if name == "" {
		return workstream.Workstream{}, errors.New("workstream name cannot be empty")
	}
	if len(roots) == 0 {
		return workstream.Workstream{}, errors.New("workstream requires at least one root")
	}
	normalized := make([]string, 0, len(roots))
	for _, value := range roots {
		root, err := validateRoot(value)
		if err != nil {
			return workstream.Workstream{}, err
		}
		if !containsRoot(normalized, root) {
			normalized = append(normalized, root)
		}
	}
	id, err := workstream.NewID()
	if err != nil {
		return workstream.Workstream{}, err
	}
	now := c.now()
	item := workstream.Workstream{
		ID: id, Name: name, Description: strings.TrimSpace(description),
		ArtifactDir: filepath.Join(c.store.ArtifactBase(), id), Roots: normalized,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := os.MkdirAll(item.ArtifactDir, 0o700); err != nil {
		return workstream.Workstream{}, fmt.Errorf("create workstream artifact directory: %w", err)
	}
	_, err = c.store.Mutate(ctx, func(state *workstream.State) (bool, error) {
		for _, existing := range state.Workstreams {
			if existing.ArchivedAt == nil && strings.EqualFold(existing.Name, name) {
				return false, fmt.Errorf("an active workstream named %q already exists", name)
			}
		}
		state.Workstreams = append(state.Workstreams, item)
		return true, nil
	})
	return item, err
}

func (c *Controller) RenameWorkstream(ctx context.Context, id, name string) error {
	name = cleanName(name)
	if name == "" {
		return errors.New("workstream name cannot be empty")
	}
	now := c.now()
	_, err := c.store.Mutate(ctx, func(state *workstream.State) (bool, error) {
		for _, existing := range state.Workstreams {
			if existing.ID != id && existing.ArchivedAt == nil && strings.EqualFold(existing.Name, name) {
				return false, fmt.Errorf("an active workstream named %q already exists", name)
			}
		}
		for index := range state.Workstreams {
			item := &state.Workstreams[index]
			if item.ID != id || item.ArchivedAt != nil {
				continue
			}
			if item.Name == name {
				return false, nil
			}
			item.Name, item.UpdatedAt = name, now
			item.Revision++
			return true, nil
		}
		return false, fmt.Errorf("active workstream %q not found", id)
	})
	return err
}

func (c *Controller) ArchiveWorkstream(ctx context.Context, id string) error {
	now := c.now()
	_, err := c.store.Mutate(ctx, func(state *workstream.State) (bool, error) {
		found := false
		for index := range state.Workstreams {
			item := &state.Workstreams[index]
			if item.ID != id || item.ArchivedAt != nil {
				continue
			}
			item.ArchivedAt, item.UpdatedAt = &now, now
			item.Revision++
			found = true
			break
		}
		if !found {
			return false, fmt.Errorf("active workstream %q not found", id)
		}
		memberships := state.Memberships[:0]
		for _, membership := range state.Memberships {
			if membership.WorkstreamID != id {
				memberships = append(memberships, membership)
			}
		}
		state.Memberships = memberships
		return true, nil
	})
	return err
}

func (c *Controller) MoveSession(ctx context.Context, sessionID, workstreamID string) error {
	now := c.now()
	_, err := c.store.Mutate(ctx, func(state *workstream.State) (bool, error) {
		if _, ok := state.Session(sessionID); !ok {
			return false, fmt.Errorf("durable session %q not found", sessionID)
		}
		if workstreamID != "" {
			item, ok := state.Workstream(workstreamID)
			if !ok || item.ArchivedAt != nil {
				return false, fmt.Errorf("active workstream %q not found", workstreamID)
			}
		}
		current := state.WorkstreamForSession(sessionID)
		if current == workstreamID {
			return false, nil
		}
		memberships := state.Memberships[:0]
		for _, membership := range state.Memberships {
			if membership.SessionID != sessionID {
				memberships = append(memberships, membership)
			}
		}
		state.Memberships = memberships
		if workstreamID != "" {
			state.Memberships = append(state.Memberships, workstream.Membership{
				WorkstreamID: workstreamID, SessionID: sessionID, JoinedAt: now,
			})
		}
		return true, nil
	})
	return err
}

// AdoptSession is the explicit migration path for a tmux runtime created by an
// older Heikou build. Unknown panes remain orphaned until the user invokes this
// action; reconciliation never adopts them automatically.
func (c *Controller) AdoptSession(ctx context.Context, sessionID, workstreamID string) (Session, error) {
	runtime, err := c.supervisor.Find(ctx, sessionID)
	if err != nil {
		return Session{}, err
	}
	now := c.now()
	createdAt := runtime.StartedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	record := workstream.SessionRecord{
		ID: runtime.ID, Backend: runtime.Backend, InitialPrompt: runtime.Prompt,
		InitialRoot: runtime.Root, CreatedAt: createdAt,
		Launch: workstream.LaunchIntent{Status: workstream.LaunchStarted},
	}
	binding := runtimeBinding(c.socket, runtime, now)
	record.Launch.Binding = &binding
	if !runtime.Alive() {
		exitCode := runtime.ExitCode
		recordedAt := runtime.EndedAt
		if recordedAt.IsZero() {
			recordedAt = now
		}
		record.Outcome = &workstream.Outcome{Kind: workstream.OutcomeExited, ExitCode: &exitCode, RecordedAt: recordedAt}
	}
	state, err := c.store.Mutate(ctx, func(state *workstream.State) (bool, error) {
		if _, exists := state.Session(runtime.ID); exists {
			return false, fmt.Errorf("session %s is already durable", runtime.ID)
		}
		if workstreamID != "" {
			item, ok := state.Workstream(workstreamID)
			if !ok || item.ArchivedAt != nil {
				return false, fmt.Errorf("active workstream %q not found", workstreamID)
			}
		}
		state.Sessions = append(state.Sessions, record)
		if workstreamID != "" {
			state.Memberships = append(state.Memberships, workstream.Membership{
				WorkstreamID: workstreamID, SessionID: runtime.ID, JoinedAt: now,
			})
		}
		return true, nil
	})
	if err != nil {
		return Session{}, err
	}
	current, ok := state.Session(runtime.ID)
	if !ok {
		return Session{}, fmt.Errorf("adopted session %s disappeared", runtime.ID)
	}
	return projectOne(state, current, &runtime), nil
}

func (c *Controller) AddRoot(ctx context.Context, workstreamID, value string) error {
	root, err := validateRoot(value)
	if err != nil {
		return err
	}
	now := c.now()
	_, err = c.store.Mutate(ctx, func(state *workstream.State) (bool, error) {
		for index := range state.Workstreams {
			item := &state.Workstreams[index]
			if item.ID != workstreamID || item.ArchivedAt != nil {
				continue
			}
			if containsRoot(item.Roots, root) {
				return false, nil
			}
			item.Roots = append(item.Roots, root)
			item.Revision++
			item.UpdatedAt = now
			return true, nil
		}
		return false, fmt.Errorf("active workstream %q not found", workstreamID)
	})
	return err
}

func (c *Controller) ReplaceRoot(ctx context.Context, workstreamID, currentValue, replacementValue string) error {
	current, err := workstream.NormalizeRoot(currentValue)
	if err != nil {
		return err
	}
	replacement, err := validateRoot(replacementValue)
	if err != nil {
		return err
	}
	now := c.now()
	_, err = c.store.Mutate(ctx, func(state *workstream.State) (bool, error) {
		for index := range state.Workstreams {
			item := &state.Workstreams[index]
			if item.ID != workstreamID || item.ArchivedAt != nil {
				continue
			}
			rootIndex := rootIndex(item.Roots, current)
			if rootIndex < 0 {
				return false, fmt.Errorf("root %q is not registered in workstream %q", current, item.Name)
			}
			if filepath.Clean(current) == filepath.Clean(replacement) {
				return false, nil
			}
			if containsRoot(item.Roots, replacement) {
				return false, fmt.Errorf("root %q is already registered in workstream %q", replacement, item.Name)
			}
			item.Roots[rootIndex] = replacement
			item.Revision++
			item.UpdatedAt = now
			return true, nil
		}
		return false, fmt.Errorf("active workstream %q not found", workstreamID)
	})
	return err
}

func (c *Controller) RemoveRoot(ctx context.Context, workstreamID, value string) error {
	root, err := workstream.NormalizeRoot(value)
	if err != nil {
		return err
	}
	now := c.now()
	_, err = c.store.Mutate(ctx, func(state *workstream.State) (bool, error) {
		for index := range state.Workstreams {
			item := &state.Workstreams[index]
			if item.ID != workstreamID || item.ArchivedAt != nil {
				continue
			}
			rootIndex := rootIndex(item.Roots, root)
			if rootIndex < 0 {
				return false, fmt.Errorf("root %q is not registered in workstream %q", root, item.Name)
			}
			if len(item.Roots) == 1 {
				return false, errors.New("a workstream must keep at least one root; edit it instead")
			}
			item.Roots = append(item.Roots[:rootIndex], item.Roots[rootIndex+1:]...)
			item.Revision++
			item.UpdatedAt = now
			return true, nil
		}
		return false, fmt.Errorf("active workstream %q not found", workstreamID)
	})
	return err
}

func project(state workstream.State, runtimes []heikou.Session, path string) Snapshot {
	runtimeByID := make(map[string]heikou.Session, len(runtimes))
	for _, runtime := range runtimes {
		runtimeByID[runtime.ID] = runtime
	}
	snapshot := Snapshot{Revision: state.Revision, StatePath: path}
	for _, item := range state.Workstreams {
		if item.ArchivedAt == nil {
			snapshot.Workstreams = append(snapshot.Workstreams, item)
		}
	}
	for _, record := range state.Sessions {
		var runtime *heikou.Session
		if observed, ok := runtimeByID[record.ID]; ok {
			copy := observed
			runtime = &copy
			delete(runtimeByID, record.ID)
		}
		snapshot.Sessions = append(snapshot.Sessions, projectOne(state, record, runtime))
	}
	for _, runtime := range runtimes {
		if _, unknown := runtimeByID[runtime.ID]; !unknown {
			continue
		}
		copy := runtime
		status := StatusExited
		if runtime.Alive() {
			status = StatusLive
		}
		snapshot.Orphans = append(snapshot.Orphans, Session{
			ID: runtime.ID, Backend: runtime.Backend, Prompt: runtime.Prompt,
			LastUserMessage: runtime.LastUserMessage, Root: runtime.Root,
			CreatedAt: runtime.StartedAt, Status: status, Orphaned: true, Runtime: &copy,
		})
	}
	sort.SliceStable(snapshot.Sessions, func(i, j int) bool {
		if snapshot.Sessions[i].Alive() != snapshot.Sessions[j].Alive() {
			return snapshot.Sessions[i].Alive()
		}
		return snapshot.Sessions[i].CreatedAt.After(snapshot.Sessions[j].CreatedAt)
	})
	return snapshot
}

func projectOne(state workstream.State, record workstream.SessionRecord, runtime *heikou.Session) Session {
	status := StatusUnavailable
	if runtime != nil {
		if runtime.Alive() {
			status = StatusLive
		} else {
			status = StatusExited
		}
	} else if record.Outcome != nil {
		switch record.Outcome.Kind {
		case workstream.OutcomeExited:
			status = StatusExited
		case workstream.OutcomeStopped:
			status = StatusStopped
		case workstream.OutcomeStartFailed:
			status = StatusStartFailed
		}
	}
	lastUserMessage := ""
	if runtime != nil {
		lastUserMessage = runtime.LastUserMessage
	}
	return Session{
		ID: record.ID, Backend: record.Backend, Prompt: record.InitialPrompt,
		LastUserMessage: lastUserMessage,
		Root:            record.InitialRoot, CreatedAt: record.CreatedAt,
		WorkstreamID: state.WorkstreamForSession(record.ID), Status: status,
		Durable: true, Record: record, Runtime: runtime,
	}
}

func runtimeBinding(socket string, runtime heikou.Session, now time.Time) workstream.RuntimeBinding {
	boundAt := runtime.StartedAt
	if boundAt.IsZero() {
		boundAt = now
	}
	return workstream.RuntimeBinding{
		Driver: "tmux", Socket: socket, SessionName: runtime.Name, BoundAt: boundAt,
	}
}

func sameBinding(left *workstream.RuntimeBinding, right workstream.RuntimeBinding) bool {
	return left != nil && left.Driver == right.Driver && left.Socket == right.Socket &&
		left.SessionName == right.SessionName
}

func validateRoot(value string) (string, error) {
	root, err := workstream.NormalizeRoot(value)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("open root %q: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("root %q is not a directory", root)
	}
	return root, nil
}

func containsRoot(roots []string, query string) bool {
	return rootIndex(roots, query) >= 0
}

func rootIndex(roots []string, query string) int {
	query = filepath.Clean(query)
	for index, root := range roots {
		if filepath.Clean(root) == query {
			return index
		}
	}
	return -1
}

func cleanName(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if len([]rune(value)) > 80 {
		value = string([]rune(value)[:80])
	}
	return value
}
