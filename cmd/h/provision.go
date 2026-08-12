package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/home"
)

const (
	// ManagersWorkstreamName is seeded once so a new installation has somewhere
	// to run pilots without the user having to build it by hand.
	ManagersWorkstreamName = "heikou-managers"

	managersWorkstreamDescription = "Agent sessions that maintain Heikou's own state."

	// provisionMarkerName records one-time setup inside the Heikou home.
	provisionMarkerName = ".provisioned"
)

// provisionRecord is the durable answer to "has Heikou already done this once?",
// which is deliberately a different question from "does it exist right now".
//
// Deleting the managers workstream is a decision Heikou honors. Without this
// record, seeding would key off the workstream's presence and resurrect it on
// the next launch, silently overriding the user.
type provisionRecord struct {
	ManagersWorkstream bool `json:"managers_workstream"`
}

func provisionMarkerPath() (string, error) {
	return home.Path(provisionMarkerName)
}

func loadProvisionRecord() (provisionRecord, error) {
	path, err := provisionMarkerPath()
	if err != nil {
		return provisionRecord{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return provisionRecord{}, nil
		}
		return provisionRecord{}, fmt.Errorf("read %q: %w", path, err)
	}
	var record provisionRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		// A marker we cannot read is treated as "already provisioned" rather than
		// as absent. Re-seeding against an unreadable marker would recreate a
		// workstream the user may have deliberately removed.
		return provisionRecord{ManagersWorkstream: true}, nil
	}
	return record, nil
}

func saveProvisionRecord(record provisionRecord) error {
	// Ensure the directory here rather than relying on a caller having done it.
	// The marker is what makes seeding once-ever, so it must not fail to persist
	// because of an ordering assumption somewhere up the stack.
	dir, err := home.Ensure()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, provisionMarkerName)
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode provisioning record: %w", err)
	}
	return writePrivateFile(path, string(data)+"\n")
}

// provisionManagersWorkstream creates the managers workstream the first time an
// installation opens, rooted only at the Heikou home directory so sessions
// started there can see AGENTS.md and the state they are meant to maintain.
//
// It runs at most once ever. A user who deletes or archives the workstream
// keeps it deleted.
func provisionManagersWorkstream(ctx context.Context, controller *control.Controller, writer io.Writer) error {
	record, err := loadProvisionRecord()
	if err != nil {
		return err
	}
	if record.ManagersWorkstream {
		return nil
	}

	dir, err := home.Ensure()
	if err != nil {
		return err
	}
	snapshot, err := controller.Snapshot(ctx)
	if err != nil {
		return err
	}
	// A workstream the user already made by this name is theirs; record the step
	// as done rather than colliding with it.
	for _, existing := range snapshot.Workstreams {
		if strings.EqualFold(existing.Name, ManagersWorkstreamName) {
			record.ManagersWorkstream = true
			return saveProvisionRecord(record)
		}
	}

	item, err := controller.CreateWorkstream(ctx, ManagersWorkstreamName, managersWorkstreamDescription, []string{dir})
	if err != nil {
		return fmt.Errorf("create the %s workstream: %w", ManagersWorkstreamName, err)
	}
	record.ManagersWorkstream = true
	if err := saveProvisionRecord(record); err != nil {
		return fmt.Errorf("created %s but did not record provisioning: %w", ManagersWorkstreamName, err)
	}
	fmt.Fprintf(writer, "heikou: created the %s workstream rooted at %s\n", item.Name, dir)
	return nil
}

// provisionInstallation performs one-time setup that needs a controller. A
// failure is reported and does not abort the command: not being able to seed a
// convenience workstream is no reason to refuse to open the dashboard.
func provisionInstallation(ctx context.Context, controller *control.Controller, writer io.Writer) {
	if err := provisionManagersWorkstream(ctx, controller, writer); err != nil {
		fmt.Fprintln(writer, "heikou:", oneLine(err.Error()))
	}
}

// forgetProvisioning is used by h init --force so an explicit re-provision can
// recreate a managers workstream the user removed.
func forgetProvisioning() error {
	path, err := provisionMarkerPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear %q: %w", path, err)
	}
	return nil
}
