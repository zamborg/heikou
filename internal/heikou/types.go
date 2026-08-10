package heikou

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Backend identifies a native coding-agent CLI.
type Backend string

const (
	BackendCodex   Backend = "codex"
	BackendClaude  Backend = "claude"
	BackendNoAgent Backend = "no-agent"
)

func ParseBackend(value string) (Backend, error) {
	switch Backend(strings.ToLower(strings.TrimSpace(value))) {
	case BackendCodex:
		return BackendCodex, nil
	case BackendClaude:
		return BackendClaude, nil
	case BackendNoAgent:
		return BackendNoAgent, nil
	default:
		return "", fmt.Errorf("unknown runner %q (want codex, claude, or no-agent)", value)
	}
}

func (b Backend) Next() Backend {
	switch b {
	case BackendCodex:
		return BackendClaude
	case BackendClaude:
		return BackendNoAgent
	default:
		return BackendCodex
	}
}

type Status string

const (
	StatusLive   Status = "live"
	StatusExited Status = "exited"
	StatusFailed Status = "failed"
)

// Session is the runner-neutral projection of one tmux-owned agent process.
// It intentionally contains process truth only; semantic states such as
// "needs input" require a future runner-specific observer.
type Session struct {
	ID              string
	Name            string
	PaneID          string
	Backend         Backend
	Prompt          string
	Root            string
	CurrentPath     string
	CurrentCommand  string
	Status          Status
	StartedAt       time.Time
	EndedAt         time.Time
	LastActivityAt  time.Time
	ExitCode        int
	AttachedClients int
	PaneInMode      bool
	InputDisabled   bool
}

func (s Session) Alive() bool { return s.Status == StatusLive }

func (s Session) Runtime(now time.Time) time.Duration {
	end := now
	if !s.EndedAt.IsZero() {
		end = s.EndedAt
	}
	if s.StartedAt.IsZero() || end.Before(s.StartedAt) {
		return 0
	}
	return end.Sub(s.StartedAt)
}

type StartRequest struct {
	// ID is allocated by the durable controller before launch. Runtime
	// implementations must use it verbatim and must never allocate identity.
	ID      string
	Backend Backend
	Prompt  string
	Root    string
	// Command is an argv prefix for agent backends. The executable is the first
	// element and fixed flags follow it. Supervisor implementations append the
	// runner-specific task/session arguments without invoking a shell. It is
	// ignored for no-agent sessions, which start tmux's default shell directly.
	Command []string
}

// Supervisor is the deliberately small boundary between the dashboard and
// today's tmux runtime. A daemon or MCP-backed runtime can implement the same
// contract later without replacing the UI.
type Supervisor interface {
	Bootstrap(context.Context) error
	Sessions(context.Context) ([]Session, error)
	Find(context.Context, string) (Session, error)
	Start(context.Context, StartRequest) (Session, error)
	Send(context.Context, Session, string) error
	Capture(context.Context, Session, int) (string, error)
	Stop(context.Context, Session) error
	AttachCommand(Session) *exec.Cmd
}
