package control

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/workstream"
)

// ActorKind identifies who requested a controller mutation. Manager mode will
// add grant-backed session actors; today every human-facing surface uses the
// same explicit local-human actor instead of bypassing this boundary.
type ActorKind string

const (
	ActorLocalHuman ActorKind = "local_human"
	ActorSession    ActorKind = "session"
)

type Actor struct {
	Kind      ActorKind
	SessionID string
}

func LocalHuman() Actor { return Actor{Kind: ActorLocalHuman} }

type ScopeKind string

const (
	ScopeInstallation ScopeKind = "installation"
	ScopeWorkstream   ScopeKind = "workstream"
)

type Scope struct {
	Kind         ScopeKind
	WorkstreamID string
}

func InstallationScope() Scope { return Scope{Kind: ScopeInstallation} }
func WorkstreamScope(id string) Scope {
	return Scope{Kind: ScopeWorkstream, WorkstreamID: strings.TrimSpace(id)}
}

// Action is deliberately closed over the typed actions in this package. This
// prevents the command plane from degrading into a string plus map[string]any.
type Action interface{ commandAction() }

type StartAction struct {
	Backend heikou.Backend
	Prompt  string
	Root    string
}

func (StartAction) commandAction() {}

// ResumeSessionAction starts a new session that continues an existing
// session's native conversation. It is a start, not an edit: the earlier
// record keeps its own history and outcome, and the new one records that it
// was handed the conversation rather than given a fresh one.
type ResumeSessionAction struct {
	SessionID string
	Prompt    string
}

func (ResumeSessionAction) commandAction() {}

// RegisterConversationAction asks Heikou to learn the conversation id a runner
// minted for a session it could not name at launch.
//
// It deliberately carries no id and no provenance. A caller that could supply
// either could assert an unverified conversation as fact, and the entire value
// of the field is that it cannot be asserted — the controller resolves it
// through the configured resolver and records how it came to know it.
type RegisterConversationAction struct {
	SessionID string
}

func (RegisterConversationAction) commandAction() {}

type SendAction struct {
	SessionID string
	Message   string
}

func (SendAction) commandAction() {}

type StopAction struct{ SessionID string }

func (StopAction) commandAction() {}

type DeleteSessionAction struct{ SessionID string }

func (DeleteSessionAction) commandAction() {}

type SetSessionTitleAction struct {
	SessionID string
	Title     string
}

func (SetSessionTitleAction) commandAction() {}

type CreateWorkstreamAction struct {
	Name        string
	Description string
	Roots       []string
}

func (CreateWorkstreamAction) commandAction() {}

type RenameWorkstreamAction struct{ Name string }

func (RenameWorkstreamAction) commandAction() {}

type ReorderWorkstreamAction struct{ Delta int }

func (ReorderWorkstreamAction) commandAction() {}

type ArchiveWorkstreamAction struct{}

func (ArchiveWorkstreamAction) commandAction() {}

type MoveSessionAction struct {
	SessionID string
	// An installation-scoped command with an empty destination moves the
	// session to Ungrouped. Otherwise the command scope is the destination.
	WorkstreamID string
}

func (MoveSessionAction) commandAction() {}

type AdoptSessionAction struct {
	SessionID    string
	WorkstreamID string
}

func (AdoptSessionAction) commandAction() {}

type AddRootAction struct{ Root string }

func (AddRootAction) commandAction() {}

type ReplaceRootAction struct {
	Current     string
	Replacement string
}

func (ReplaceRootAction) commandAction() {}

type RemoveRootAction struct{ Root string }

func (RemoveRootAction) commandAction() {}

type Command struct {
	Actor  Actor
	Scope  Scope
	Action Action
}

type CommandResult struct {
	Session      Session
	Workstream   workstream.Workstream
	Conversation workstream.Conversation
	Moved        bool
}

// Authorizer is the future manager-policy seam. The V0.3.4 policy admits only
// local humans; a later grant authorizer can admit scoped session actors
// without adding another execution path.
type Authorizer interface {
	Authorize(context.Context, Command) error
}

type AuthorizeFunc func(context.Context, Command) error

func (f AuthorizeFunc) Authorize(ctx context.Context, command Command) error {
	return f(ctx, command)
}

type localHumanAuthorizer struct{}

func (localHumanAuthorizer) Authorize(_ context.Context, command Command) error {
	if command.Actor.Kind != ActorLocalHuman || command.Actor.SessionID != "" {
		return errors.New("session actors require a manager grant; manager mode is not enabled")
	}
	return nil
}

// CommandResolver supplies a trusted, snapshotted argv prefix for new native
// runners. Callers choose a backend, never executable argv.
type CommandResolver interface {
	Resolve(context.Context, heikou.Backend) ([]string, error)
}

type ResolveCommandFunc func(context.Context, heikou.Backend) ([]string, error)

func (f ResolveCommandFunc) Resolve(ctx context.Context, backend heikou.Backend) ([]string, error) {
	return f(ctx, backend)
}

// ConversationResolver discovers the conversation id a runner minted for a
// session Heikou could not name at launch. It is an interface here, and
// implemented over runner-written files in cmd/h, for the same reason
// CommandResolver is: the controller owns the policy — what may be recorded and
// with what provenance — while reading another program's files stays outside it.
//
// An implementation must return an error rather than a best guess. The
// controller writes whatever it returns into durable state and resumes against
// it, so a plausible-but-wrong id is worse than no answer.
type ConversationResolver interface {
	Resolve(context.Context, workstream.SessionRecord) (string, error)
}

type ResolveConversationFunc func(context.Context, workstream.SessionRecord) (string, error)

func (f ResolveConversationFunc) Resolve(ctx context.Context, record workstream.SessionRecord) (string, error) {
	return f(ctx, record)
}

type controllerOption func(*Controller)

func WithAuthorizer(authorizer Authorizer) controllerOption {
	return func(controller *Controller) {
		if authorizer != nil {
			controller.authorizer = authorizer
		}
	}
}

func WithCommandResolver(resolver CommandResolver) controllerOption {
	return func(controller *Controller) {
		controller.commandResolver = resolver
	}
}

func WithConversationResolver(resolver ConversationResolver) controllerOption {
	return func(controller *Controller) {
		controller.conversationResolver = resolver
	}
}

func validateCommand(command Command) error {
	if command.Action == nil {
		return errors.New("command action is required")
	}
	switch command.Actor.Kind {
	case ActorLocalHuman:
		if command.Actor.SessionID != "" {
			return errors.New("local-human actor cannot carry a session id")
		}
	case ActorSession:
		if !workstream.ValidID(command.Actor.SessionID) {
			return errors.New("session actor requires a valid session id")
		}
	default:
		return fmt.Errorf("unknown command actor %q", command.Actor.Kind)
	}
	switch command.Scope.Kind {
	case ScopeInstallation:
		if command.Scope.WorkstreamID != "" {
			return errors.New("installation scope cannot carry a workstream id")
		}
	case ScopeWorkstream:
		if !workstream.ValidID(command.Scope.WorkstreamID) {
			return errors.New("workstream scope requires a valid workstream id")
		}
	default:
		return fmt.Errorf("unknown command scope %q", command.Scope.Kind)
	}
	return validateActionScope(command)
}

func validateActionScope(command Command) error {
	requireWorkstream := func() error {
		if command.Scope.Kind != ScopeWorkstream {
			return errors.New("command requires workstream scope")
		}
		return nil
	}
	switch action := command.Action.(type) {
	// A resume is a start: either scope is meaningful, because the scope names
	// where the new session lands.
	case StartAction, ResumeSessionAction:
		return nil
	case SendAction, StopAction, DeleteSessionAction, SetSessionTitleAction,
		RegisterConversationAction:
		return nil
	case CreateWorkstreamAction:
		if command.Scope.Kind != ScopeInstallation {
			return errors.New("create workstream requires installation scope")
		}
	case RenameWorkstreamAction, ReorderWorkstreamAction, ArchiveWorkstreamAction,
		AddRootAction, ReplaceRootAction, RemoveRootAction:
		return requireWorkstream()
	case MoveSessionAction:
		if action.WorkstreamID == "" {
			if command.Scope.Kind != ScopeInstallation {
				return errors.New("moving to Ungrouped requires installation scope")
			}
		} else if command.Scope.Kind != ScopeWorkstream || command.Scope.WorkstreamID != action.WorkstreamID {
			return errors.New("move-session scope must match its destination workstream")
		}
	case AdoptSessionAction:
		if action.WorkstreamID == "" {
			if command.Scope.Kind != ScopeInstallation {
				return errors.New("adopting into Ungrouped requires installation scope")
			}
		} else if command.Scope.Kind != ScopeWorkstream || command.Scope.WorkstreamID != action.WorkstreamID {
			return errors.New("adopt-session scope must match its destination workstream")
		}
	default:
		return fmt.Errorf("unsupported command action %T", command.Action)
	}
	return nil
}

func scopeForWorkstream(id string) Scope {
	if strings.TrimSpace(id) == "" {
		return InstallationScope()
	}
	return WorkstreamScope(id)
}
