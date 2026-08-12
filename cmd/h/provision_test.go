package main

import (
	"os"
	"path/filepath"
	"testing"
)

func isolateHome(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("HEIKOU_HOME", filepath.Join(base, ".heikou"))
	return filepath.Join(base, ".heikou")
}

// The marker answers "has Heikou seeded this once", which is deliberately a
// different question from "does the workstream exist now". Conflating them
// would resurrect a workstream the user removed on purpose.
func TestProvisionRecordRoundTrip(t *testing.T) {
	isolateHome(t)

	record, err := loadProvisionRecord()
	if err != nil {
		t.Fatalf("load on a fresh install: %v", err)
	}
	if record.ManagersWorkstream {
		t.Fatal("a fresh install must not look already provisioned")
	}

	if err := saveProvisionRecord(provisionRecord{ManagersWorkstream: true}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if record, err = loadProvisionRecord(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !record.ManagersWorkstream {
		t.Fatal("the recorded step did not survive a reload")
	}
}

// Seeding is skipped when the marker cannot be parsed. Re-seeding on a damaged
// marker would recreate a workstream the user may have deliberately deleted,
// and silently overriding that decision is worse than skipping convenience setup.
func TestProvisionRecordFailsSafeOnDamage(t *testing.T) {
	dir := isolateHome(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, provisionMarkerName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	record, err := loadProvisionRecord()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !record.ManagersWorkstream {
		t.Fatal("a damaged marker must be treated as already provisioned, not as absent")
	}
}

func TestForgetProvisioningAllowsDeliberateReseed(t *testing.T) {
	dir := isolateHome(t)
	if err := saveProvisionRecord(provisionRecord{ManagersWorkstream: true}); err != nil {
		t.Fatal(err)
	}
	if err := forgetProvisioning(); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, provisionMarkerName)); !os.IsNotExist(err) {
		t.Fatal("the marker survived forgetProvisioning")
	}
	record, err := loadProvisionRecord()
	if err != nil {
		t.Fatal(err)
	}
	if record.ManagersWorkstream {
		t.Fatal("provisioning still looks done after being forgotten")
	}
	// Forgetting an absent marker is not an error, so h init --force is repeatable.
	if err := forgetProvisioning(); err != nil {
		t.Fatalf("second forget: %v", err)
	}
}

func TestProvisionMarkerIsPrivate(t *testing.T) {
	dir := isolateHome(t)
	if err := saveProvisionRecord(provisionRecord{ManagersWorkstream: true}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, provisionMarkerName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("marker mode = %o, want 600", info.Mode().Perm())
	}
}
