package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/home"
	"github.com/zamborg/heikou/internal/workstream"
)

const (
	// ManagersWorkstreamName is seeded once so a new installation has somewhere
	// to run pilots without the user having to build it by hand.
	ManagersWorkstreamName = "heikou-managers"

	managersWorkstreamDescription = "Agent sessions that maintain Heikou's own state."
)

// provisionInstallation seeds the managers workstream on an installation that
// has never written durable state.
//
// The state file is the marker. Reads never create it and no-op mutations never
// write it, so its absence means "nothing has ever been recorded here" — and its
// presence means the user has state, whether or not this particular workstream
// is part of it. Deleting the workstream therefore keeps it deleted, with no
// separate provisioning record to store, corrupt, or keep in sync.
//
// A failure is reported and does not abort the command: not being able to seed
// a convenience workstream is no reason to refuse to open the dashboard.
func provisionInstallation(ctx context.Context, controller *control.Controller, store workstream.FileStore, writer io.Writer) {
	if store.Exists() {
		return
	}
	if err := seedManagersWorkstream(ctx, controller, writer); err != nil {
		fmt.Fprintln(writer, "heikou:", oneLine(err.Error()))
	}
}

// seedManagersWorkstream creates the workstream rooted only at the Heikou home
// directory, so sessions started there can see AGENTS.md and the state they are
// meant to maintain.
func seedManagersWorkstream(ctx context.Context, controller *control.Controller, writer io.Writer) error {
	dir, err := home.Ensure()
	if err != nil {
		return err
	}
	item, err := controller.CreateWorkstream(ctx, ManagersWorkstreamName, managersWorkstreamDescription, []string{dir})
	if err != nil {
		return fmt.Errorf("create the %s workstream: %w", ManagersWorkstreamName, err)
	}
	fmt.Fprintf(writer, "heikou: created the %s workstream rooted at %s\n", item.Name, dir)
	return nil
}

// reprovisionManagersWorkstream is the explicit opt-in behind h init. An
// established installation is never seeded implicitly — injecting a workstream
// into state someone already organized would be presumptuous — so this is how a
// user asks for it, and how they get it back after deleting it.
func reprovisionManagersWorkstream(ctx context.Context, controller *control.Controller, writer io.Writer) error {
	snapshot, err := controller.Snapshot(ctx)
	if err != nil {
		return err
	}
	for _, existing := range snapshot.Workstreams {
		if strings.EqualFold(existing.Name, ManagersWorkstreamName) {
			fmt.Fprintf(writer, "the %s workstream already exists\n", ManagersWorkstreamName)
			return nil
		}
	}
	return seedManagersWorkstream(ctx, controller, writer)
}
