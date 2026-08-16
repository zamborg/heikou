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

// ConversationID is the id the runner filed this session's conversation under,
// which is what anything reading a runner-written file has to ask for.
//
// It is not always the durable session id, and the difference is invisible
// until it is wrong. A session Heikou launched fresh is `claude --session-id
// <durable id>`, so the two are equal. A resumed session is launched
// `--resume <conversation id>` with a durable id of its own, so Claude appends
// to the file named for the conversation and nothing is ever written under the
// new session's id. Asking by the durable id there finds no file, forever.
//
// The registration's Source is deliberately not consulted. It separates an id
// Heikou caused from one it matched against a runner's files, which is what
// must not be blurred when Heikou states provenance — but an observed id is
// precisely the id that names a file on disk, so it is the better answer to
// this question and not a worse one.
func (s Session) ConversationID() string {
	if s.Record.Conversation != nil {
		if id := strings.TrimSpace(s.Record.Conversation.ID); id != "" {
			return id
		}
	}
	return s.ID
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
	if s.Runtime != nil && !s.Runtime.Alive() && s.Runtime.ExitCode != nil {
		return *s.Runtime.ExitCode, true
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
	WorkstreamID string
	// resume names a native conversation the new session continues. It is
	// unexported because it is never a caller's choice: it is filled by the
	// resume path from a conversation Heikou already registered, so no surface
	// can start a session against an id nobody verified.
	resume string
}

type Service interface {
	Snapshot(context.Context) (Snapshot, error)
	Find(context.Context, string) (Session, error)
	Start(context.Context, StartRequest) (Session, error)
	ResumeSession(context.Context, string, string) (Session, error)
	RegisterConversation(context.Context, string) (workstream.Conversation, error)
	Send(context.Context, string, string) error
	Capture(context.Context, string, int) (string, error)
	Stop(context.Context, string) error
	DeleteSession(context.Context, string) error
	SetSessionTitle(context.Context, string, string) error
	AttachCommand(context.Context, string) (*exec.Cmd, error)
	CreateWorkstream(context.Context, string, string, []string) (workstream.Workstream, error)
	RenameWorkstream(context.Context, string, string) error
	ReorderWorkstream(context.Context, string, int) (bool, error)
	ArchiveWorkstream(context.Context, string) error
	MoveSession(context.Context, string, string) error
	AdoptSession(context.Context, string, string) (Session, error)
	AddRoot(context.Context, string, string) error
	ReplaceRoot(context.Context, string, string, string) error
	RemoveRoot(context.Context, string, string) error
}

type Controller struct {
	supervisor           heikou.Supervisor
	store                workstream.Repository
	socket               string
	now                  func() time.Time
	authorizer           Authorizer
	commandResolver      CommandResolver
	conversationResolver ConversationResolver
}

func New(supervisor heikou.Supervisor, store workstream.Repository, socket string, options ...controllerOption) *Controller {
	controller := &Controller{
		supervisor: supervisor, store: store, socket: socket, now: time.Now,
		authorizer: localHumanAuthorizer{},
	}
	for _, option := range options {
		if option != nil {
			option(controller)
		}
	}
	return controller
}

// Execute is the single mutation path for human callers and future
// grant-backed manager sessions. Reads remain explicit query methods.
func (c *Controller) Execute(ctx context.Context, command Command) (CommandResult, error) {
	if err := validateCommand(command); err != nil {
		return CommandResult{}, err
	}
	if c.authorizer == nil {
		return CommandResult{}, errors.New("command authorizer is not configured")
	}
	if err := c.authorizer.Authorize(ctx, command); err != nil {
		return CommandResult{}, fmt.Errorf("authorize command: %w", err)
	}

	switch action := command.Action.(type) {
	case StartAction:
		session, err := c.start(ctx, StartRequest{
			Backend: action.Backend, Prompt: action.Prompt, Root: action.Root,
			WorkstreamID: command.Scope.WorkstreamID,
		})
		return CommandResult{Session: session}, err
	case ResumeSessionAction:
		session, err := c.resumeSession(ctx, action.SessionID, action.Prompt, command.Scope.WorkstreamID)
		return CommandResult{Session: session}, err
	case RegisterConversationAction:
		conversation, err := c.registerConversation(ctx, action.SessionID)
		return CommandResult{Conversation: conversation}, err
	case SendAction:
		return CommandResult{}, c.send(ctx, action.SessionID, action.Message)
	case StopAction:
		return CommandResult{}, c.stop(ctx, action.SessionID)
	case DeleteSessionAction:
		return CommandResult{}, c.deleteSession(ctx, action.SessionID)
	case SetSessionTitleAction:
		return CommandResult{}, c.setSessionTitle(ctx, action.SessionID, action.Title)
	case CreateWorkstreamAction:
		item, err := c.createWorkstream(ctx, action.Name, action.Description, action.Roots)
		return CommandResult{Workstream: item}, err
	case RenameWorkstreamAction:
		return CommandResult{}, c.renameWorkstream(ctx, command.Scope.WorkstreamID, action.Name)
	case ReorderWorkstreamAction:
		moved, err := c.reorderWorkstream(ctx, command.Scope.WorkstreamID, action.Delta)
		return CommandResult{Moved: moved}, err
	case ArchiveWorkstreamAction:
		return CommandResult{}, c.archiveWorkstream(ctx, command.Scope.WorkstreamID)
	case MoveSessionAction:
		return CommandResult{}, c.moveSession(ctx, action.SessionID, action.WorkstreamID)
	case AdoptSessionAction:
		session, err := c.adoptSession(ctx, action.SessionID, action.WorkstreamID)
		return CommandResult{Session: session}, err
	case AddRootAction:
		return CommandResult{}, c.addRoot(ctx, command.Scope.WorkstreamID, action.Root)
	case ReplaceRootAction:
		return CommandResult{}, c.replaceRoot(ctx, command.Scope.WorkstreamID, action.Current, action.Replacement)
	case RemoveRootAction:
		return CommandResult{}, c.removeRoot(ctx, command.Scope.WorkstreamID, action.Root)
	default:
		return CommandResult{}, fmt.Errorf("unsupported command action %T", command.Action)
	}
}

func humanCommand(scope Scope, action Action) Command {
	return Command{Actor: LocalHuman(), Scope: scope, Action: action}
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
			if runtime.ExitCode == nil {
				if record.Outcome != nil && record.Outcome.Kind == workstream.OutcomeStartFailed {
					record.Outcome = nil
					changed = true
				}
				continue
			}
			if record.Outcome == nil || record.Outcome.Kind == workstream.OutcomeStartFailed {
				exitCode := *runtime.ExitCode
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
	result, err := c.Execute(ctx, humanCommand(scopeForWorkstream(request.WorkstreamID), StartAction{
		Backend: request.Backend, Prompt: request.Prompt, Root: request.Root,
	}))
	return result.Session, err
}

func (c *Controller) start(ctx context.Context, request StartRequest) (Session, error) {
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
	prompt := request.Prompt
	if strings.TrimSpace(prompt) == "" {
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
	_, err = c.store.Mutate(ctx, func(state *workstream.State) (bool, error) {
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

	var runtime heikou.Session
	var launchErr error
	var command []string
	startAttempted := false
	if request.Backend != heikou.BackendNoAgent && c.commandResolver != nil {
		command, launchErr = c.commandResolver.Resolve(ctx, request.Backend)
	}
	if launchErr == nil {
		startAttempted = true
		runtime, launchErr = c.supervisor.Start(ctx, heikou.StartRequest{
			ID: id, Backend: request.Backend, Prompt: prompt, Root: root, Command: command,
			Resume: request.resume,
		})
	}
	if launchErr != nil && startAttempted {
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
	var state workstream.State
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
			// The conversation is registered in the same mutation that records
			// the binding, because it is known for exactly the same reason: the
			// launch happened and Heikou chose what it passed. Nothing is read
			// back from the runner to establish it.
			if conversation := assignedConversation(request, id, boundAt); conversation != nil {
				state.Sessions[index].Conversation = conversation
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

// assignedConversation returns the conversation a launch is entitled to claim
// without asking anyone, or nil when the runner did not let Heikou name one.
//
// There are exactly two such cases, and both are facts Heikou caused:
//
//   - a resume, for any runner, passes the conversation id on the command line,
//     so the session continues that conversation by construction;
//   - a fresh Claude session is launched as `claude --session-id <durable id>`,
//     so Claude's conversation id is Heikou's session id.
//
// A fresh Codex session gets nil. Codex has no flag for choosing a session id —
// verified against codex 0.145, which rejects --session-id outright — so its
// conversation can only be observed later, and inventing one here would be the
// exact failure the source field exists to prevent.
func assignedConversation(request StartRequest, id string, at time.Time) *workstream.Conversation {
	switch {
	case strings.TrimSpace(request.resume) != "":
		return &workstream.Conversation{
			ID: request.resume, Source: workstream.ConversationAssigned, RecordedAt: at,
		}
	case request.Backend == heikou.BackendClaude:
		return &workstream.Conversation{
			ID: id, Source: workstream.ConversationAssigned, RecordedAt: at,
		}
	default:
		return nil
	}
}

// RegisterConversation learns the conversation id a runner minted for a session
// Heikou could not name at launch, and records it durably.
//
// It is idempotent: a session that already has a registration returns it
// unchanged rather than re-deriving it. That matters beyond saving work — the
// resolver matches against files the runner owns, and re-running it later could
// find a different answer as those files come and go, so the first answer that
// could be proven is the one that is kept.
func (c *Controller) RegisterConversation(ctx context.Context, id string) (workstream.Conversation, error) {
	result, err := c.Execute(ctx, humanCommand(InstallationScope(), RegisterConversationAction{SessionID: id}))
	return result.Conversation, err
}

func (c *Controller) registerConversation(ctx context.Context, id string) (workstream.Conversation, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return workstream.Conversation{}, errors.New("session id is required")
	}
	state, err := c.store.Load(ctx)
	if err != nil {
		return workstream.Conversation{}, err
	}
	record, ok := state.Session(id)
	if !ok {
		return workstream.Conversation{}, fmt.Errorf("durable session %q not found", id)
	}
	if record.Conversation != nil {
		return *record.Conversation, nil
	}
	if record.Backend == heikou.BackendNoAgent {
		return workstream.Conversation{}, fmt.Errorf(
			"session %q runs a plain shell, which records no conversation to resume", id)
	}
	if c.conversationResolver == nil {
		return workstream.Conversation{}, errors.New("no conversation resolver is configured")
	}
	resolved, err := c.conversationResolver.Resolve(ctx, record)
	if err != nil {
		return workstream.Conversation{}, err
	}
	if strings.TrimSpace(resolved) == "" {
		return workstream.Conversation{}, errors.New("conversation resolver returned an empty id")
	}

	conversation := workstream.Conversation{
		ID: resolved, Source: workstream.ConversationObserved, RecordedAt: c.now(),
	}
	_, err = c.store.Mutate(ctx, func(state *workstream.State) (bool, error) {
		for index := range state.Sessions {
			if state.Sessions[index].ID != id {
				continue
			}
			// Another process may have registered while the resolver ran. Its
			// answer wins, because two writers racing to record an observation
			// should converge rather than take turns overwriting each other.
			if existing := state.Sessions[index].Conversation; existing != nil {
				conversation = *existing
				return false, nil
			}
			state.Sessions[index].Conversation = &conversation
			return true, nil
		}
		return false, fmt.Errorf("durable session %q not found", id)
	})
	if err != nil {
		return workstream.Conversation{}, err
	}
	return conversation, nil
}

// ResumeSession starts a new session continuing an existing session's native
// conversation. The earlier record is left exactly as it is: it is the history
// of what already happened, and a resume is new work, not a correction.
func (c *Controller) ResumeSession(ctx context.Context, id, prompt string) (Session, error) {
	state, err := c.store.Load(ctx)
	if err != nil {
		return Session{}, err
	}
	workstreamID := state.WorkstreamForSession(strings.TrimSpace(id))
	result, err := c.Execute(ctx, humanCommand(scopeForWorkstream(workstreamID), ResumeSessionAction{
		SessionID: id, Prompt: prompt,
	}))
	return result.Session, err
}

func (c *Controller) resumeSession(ctx context.Context, id, prompt, workstreamID string) (Session, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Session{}, errors.New("session id is required")
	}
	if strings.TrimSpace(prompt) == "" {
		return Session{}, errors.New("prompt cannot be empty")
	}
	state, err := c.store.Load(ctx)
	if err != nil {
		return Session{}, err
	}
	record, ok := state.Session(id)
	if !ok {
		return Session{}, fmt.Errorf("durable session %q not found", id)
	}
	// Registering here is what makes resume work without the user ever handling
	// a runner id: a Claude session already carries one, and a Codex session
	// gets one resolved on first use.
	conversation, err := c.registerConversation(ctx, id)
	if err != nil {
		return Session{}, fmt.Errorf("resume session %q: %w", id, err)
	}
	return c.start(ctx, StartRequest{
		Backend: record.Backend, Prompt: prompt, Root: record.InitialRoot,
		WorkstreamID: workstreamID, resume: conversation.ID,
	})
}

func (c *Controller) Send(ctx context.Context, id, message string) error {
	_, err := c.Execute(ctx, humanCommand(InstallationScope(), SendAction{SessionID: id, Message: message}))
	return err
}

func (c *Controller) send(ctx context.Context, id, message string) error {
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
	_, err := c.Execute(ctx, humanCommand(InstallationScope(), StopAction{SessionID: id}))
	return err
}

func (c *Controller) stop(ctx context.Context, id string) error {
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
	_, err := c.Execute(ctx, humanCommand(InstallationScope(), DeleteSessionAction{SessionID: id}))
	return err
}

func (c *Controller) deleteSession(ctx context.Context, id string) error {
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
		// Every other refusal names the way forward, and this message is shown
		// to the user verbatim, so it says how to proceed rather than only what
		// went wrong.
		return fmt.Errorf("cannot delete session %q while its tmux runtime exists; stop it first", id)
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

// SetSessionTitle sets human-owned durable display identity. Empty clears the
// title; runtime names and native provider conversation identities never move.
func (c *Controller) SetSessionTitle(ctx context.Context, id, title string) error {
	_, err := c.Execute(ctx, humanCommand(InstallationScope(), SetSessionTitleAction{
		SessionID: id, Title: title,
	}))
	return err
}

func (c *Controller) setSessionTitle(ctx context.Context, id, title string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("session id is required")
	}
	title = cleanTitle(title)
	_, err := c.store.Mutate(ctx, func(state *workstream.State) (bool, error) {
		for index := range state.Sessions {
			if state.Sessions[index].ID != id {
				continue
			}
			if state.Sessions[index].Title == title {
				return false, nil
			}
			state.Sessions[index].Title = title
			return true, nil
		}
		return false, fmt.Errorf("durable session %q not found", id)
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
	result, err := c.Execute(ctx, humanCommand(InstallationScope(), CreateWorkstreamAction{
		Name: name, Description: description, Roots: roots,
	}))
	return result.Workstream, err
}

func (c *Controller) createWorkstream(ctx context.Context, name, description string, roots []string) (workstream.Workstream, error) {
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
	_, err := c.Execute(ctx, humanCommand(WorkstreamScope(id), RenameWorkstreamAction{Name: name}))
	return err
}

func (c *Controller) renameWorkstream(ctx context.Context, id, name string) error {
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

// ReorderWorkstream moves an active workstream by one visible position. The
// state slice is the durable display order, so existing state files need no
// migration and archived workstreams do not occupy a position in the UI.
func (c *Controller) ReorderWorkstream(ctx context.Context, id string, delta int) (bool, error) {
	result, err := c.Execute(ctx, humanCommand(WorkstreamScope(id), ReorderWorkstreamAction{Delta: delta}))
	return result.Moved, err
}

func (c *Controller) reorderWorkstream(ctx context.Context, id string, delta int) (bool, error) {
	if delta != -1 && delta != 1 {
		return false, fmt.Errorf("workstream reorder delta must be -1 or 1 (got %d)", delta)
	}
	moved := false
	_, err := c.store.Mutate(ctx, func(state *workstream.State) (bool, error) {
		active := make([]int, 0, len(state.Workstreams))
		position := -1
		for index, item := range state.Workstreams {
			if item.ArchivedAt != nil {
				continue
			}
			if item.ID == id {
				position = len(active)
			}
			active = append(active, index)
		}
		if position == -1 {
			return false, fmt.Errorf("active workstream %q not found", id)
		}
		target := position + delta
		if target < 0 || target >= len(active) {
			return false, nil
		}
		left, right := active[position], active[target]
		state.Workstreams[left], state.Workstreams[right] = state.Workstreams[right], state.Workstreams[left]
		moved = true
		return true, nil
	})
	return moved && err == nil, err
}

func (c *Controller) ArchiveWorkstream(ctx context.Context, id string) error {
	_, err := c.Execute(ctx, humanCommand(WorkstreamScope(id), ArchiveWorkstreamAction{}))
	return err
}

func (c *Controller) archiveWorkstream(ctx context.Context, id string) error {
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
	_, err := c.Execute(ctx, humanCommand(scopeForWorkstream(workstreamID), MoveSessionAction{
		SessionID: sessionID, WorkstreamID: workstreamID,
	}))
	return err
}

func (c *Controller) moveSession(ctx context.Context, sessionID, workstreamID string) error {
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
	result, err := c.Execute(ctx, humanCommand(scopeForWorkstream(workstreamID), AdoptSessionAction{
		SessionID: sessionID, WorkstreamID: workstreamID,
	}))
	return result.Session, err
}

func (c *Controller) adoptSession(ctx context.Context, sessionID, workstreamID string) (Session, error) {
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
	if !runtime.Alive() && runtime.ExitCode != nil {
		exitCode := *runtime.ExitCode
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
	_, err := c.Execute(ctx, humanCommand(WorkstreamScope(workstreamID), AddRootAction{Root: value}))
	return err
}

func (c *Controller) addRoot(ctx context.Context, workstreamID, value string) error {
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
	_, err := c.Execute(ctx, humanCommand(WorkstreamScope(workstreamID), ReplaceRootAction{
		Current: currentValue, Replacement: replacementValue,
	}))
	return err
}

func (c *Controller) replaceRoot(ctx context.Context, workstreamID, currentValue, replacementValue string) error {
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
	_, err := c.Execute(ctx, humanCommand(WorkstreamScope(workstreamID), RemoveRootAction{Root: value}))
	return err
}

func (c *Controller) removeRoot(ctx context.Context, workstreamID, value string) error {
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

func cleanTitle(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > workstream.MaxSessionTitleRunes {
		value = string(runes[:workstream.MaxSessionTitleRunes])
	}
	return value
}
