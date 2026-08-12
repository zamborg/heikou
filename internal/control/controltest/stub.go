// Package controltest provides the one test double for control.Service.
//
// Before it existed, each package that needed a fake controller wrote its own,
// which meant adding a method to control.Service broke every one of them and
// each had to be fixed the same way. Worse, a fake written for one package
// tended to answer differently from a fake written for another, so two tests
// could disagree about what the controller does without either being wrong.
//
// Stub is a struct of function fields rather than an interface implementation
// to be embedded and overridden wholesale: a test sets only the methods it
// cares about, and the rest answer harmlessly. Adding a method to
// control.Service now breaks exactly one file, and the compiler says so.
package controltest

import (
	"context"
	"os/exec"

	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/workstream"
)

// Stub implements control.Service. A zero Stub answers every call with zero
// values and no error, which is what a test that only exercises argument
// handling wants: the call succeeds and reveals nothing. The two exceptions
// are documented on the methods that make them.
//
// Set a field to control one method. A test that needs to observe a call
// usually embeds Stub in its own type and overrides the method there, so the
// recorded fields stay next to the assertions that read them.
type Stub struct {
	SnapshotFunc          func(context.Context) (control.Snapshot, error)
	FindFunc              func(context.Context, string) (control.Session, error)
	StartFunc             func(context.Context, control.StartRequest) (control.Session, error)
	SendFunc              func(context.Context, string, string) error
	CaptureFunc           func(context.Context, string, int) (string, error)
	StopFunc              func(context.Context, string) error
	DeleteSessionFunc     func(context.Context, string) error
	SetSessionTitleFunc   func(context.Context, string, string) error
	AttachCommandFunc     func(context.Context, string) (*exec.Cmd, error)
	CreateWorkstreamFunc  func(context.Context, string, string, []string) (workstream.Workstream, error)
	RenameWorkstreamFunc  func(context.Context, string, string) error
	ReorderWorkstreamFunc func(context.Context, string, int) (bool, error)
	ArchiveWorkstreamFunc func(context.Context, string) error
	MoveSessionFunc       func(context.Context, string, string) error
	AdoptSessionFunc      func(context.Context, string, string) (control.Session, error)
	AddRootFunc           func(context.Context, string, string) error
	ReplaceRootFunc       func(context.Context, string, string, string) error
	RemoveRootFunc        func(context.Context, string, string) error
}

// Compile-time proof that the stub keeps up with the interface. This is the
// assertion that turns "control.Service grew a method" into one build error in
// one file rather than a scattering of them.
var _ control.Service = (*Stub)(nil)

func (s *Stub) Snapshot(ctx context.Context) (control.Snapshot, error) {
	if s.SnapshotFunc != nil {
		return s.SnapshotFunc(ctx)
	}
	return control.Snapshot{}, nil
}

func (s *Stub) Find(ctx context.Context, query string) (control.Session, error) {
	if s.FindFunc != nil {
		return s.FindFunc(ctx, query)
	}
	return control.Session{}, nil
}

func (s *Stub) Start(ctx context.Context, request control.StartRequest) (control.Session, error) {
	if s.StartFunc != nil {
		return s.StartFunc(ctx, request)
	}
	return control.Session{}, nil
}

func (s *Stub) Send(ctx context.Context, id, message string) error {
	if s.SendFunc != nil {
		return s.SendFunc(ctx, id, message)
	}
	return nil
}

func (s *Stub) Capture(ctx context.Context, id string, lines int) (string, error) {
	if s.CaptureFunc != nil {
		return s.CaptureFunc(ctx, id, lines)
	}
	return "", nil
}

func (s *Stub) Stop(ctx context.Context, id string) error {
	if s.StopFunc != nil {
		return s.StopFunc(ctx, id)
	}
	return nil
}

func (s *Stub) DeleteSession(ctx context.Context, id string) error {
	if s.DeleteSessionFunc != nil {
		return s.DeleteSessionFunc(ctx, id)
	}
	return nil
}

func (s *Stub) SetSessionTitle(ctx context.Context, id, title string) error {
	if s.SetSessionTitleFunc != nil {
		return s.SetSessionTitleFunc(ctx, id, title)
	}
	return nil
}

// AttachCommand answers with a command that succeeds and does nothing, because
// a test that reaches attach is checking that it got there, not watching a
// terminal take over the process.
func (s *Stub) AttachCommand(ctx context.Context, id string) (*exec.Cmd, error) {
	if s.AttachCommandFunc != nil {
		return s.AttachCommandFunc(ctx, id)
	}
	return exec.Command("true"), nil
}

func (s *Stub) CreateWorkstream(ctx context.Context, name, description string, roots []string) (workstream.Workstream, error) {
	if s.CreateWorkstreamFunc != nil {
		return s.CreateWorkstreamFunc(ctx, name, description, roots)
	}
	return workstream.Workstream{}, nil
}

func (s *Stub) RenameWorkstream(ctx context.Context, id, name string) error {
	if s.RenameWorkstreamFunc != nil {
		return s.RenameWorkstreamFunc(ctx, id, name)
	}
	return nil
}

// ReorderWorkstream reports that the workstream moved. The zero value would
// mean "already at the edge", which is a specific outcome a caller renders
// differently, and a stub should not assert a specific outcome by accident.
func (s *Stub) ReorderWorkstream(ctx context.Context, id string, delta int) (bool, error) {
	if s.ReorderWorkstreamFunc != nil {
		return s.ReorderWorkstreamFunc(ctx, id, delta)
	}
	return true, nil
}

func (s *Stub) ArchiveWorkstream(ctx context.Context, id string) error {
	if s.ArchiveWorkstreamFunc != nil {
		return s.ArchiveWorkstreamFunc(ctx, id)
	}
	return nil
}

func (s *Stub) MoveSession(ctx context.Context, sessionID, workstreamID string) error {
	if s.MoveSessionFunc != nil {
		return s.MoveSessionFunc(ctx, sessionID, workstreamID)
	}
	return nil
}

func (s *Stub) AdoptSession(ctx context.Context, sessionID, workstreamID string) (control.Session, error) {
	if s.AdoptSessionFunc != nil {
		return s.AdoptSessionFunc(ctx, sessionID, workstreamID)
	}
	return control.Session{}, nil
}

func (s *Stub) AddRoot(ctx context.Context, workstreamID, root string) error {
	if s.AddRootFunc != nil {
		return s.AddRootFunc(ctx, workstreamID, root)
	}
	return nil
}

func (s *Stub) ReplaceRoot(ctx context.Context, workstreamID, current, replacement string) error {
	if s.ReplaceRootFunc != nil {
		return s.ReplaceRootFunc(ctx, workstreamID, current, replacement)
	}
	return nil
}

func (s *Stub) RemoveRoot(ctx context.Context, workstreamID, root string) error {
	if s.RemoveRootFunc != nil {
		return s.RemoveRootFunc(ctx, workstreamID, root)
	}
	return nil
}
