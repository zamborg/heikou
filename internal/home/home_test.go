package home

import (
	"os"
	"path/filepath"
	"testing"
)

// isolate points every path input at a temporary home so a test can never touch
// a developer's real installation.
func isolate(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv(PathEnv, "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HEIKOU_CONFIG", "")
	t.Setenv("HEIKOU_STATE", "")
	t.Setenv("HEIKOU_DATA", "")
	return base
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func TestDirDefaultsToDotHeikou(t *testing.T) {
	base := isolate(t)
	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if want := filepath.Join(base, DirName); dir != want {
		t.Fatalf("Dir = %q, want %q", dir, want)
	}
}

func TestDirHonorsOverride(t *testing.T) {
	isolate(t)
	custom := t.TempDir()
	t.Setenv(PathEnv, custom)
	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if dir != custom {
		t.Fatalf("Dir = %q, want %q", dir, custom)
	}
}

func TestMigrateMovesLegacyLayout(t *testing.T) {
	base := isolate(t)
	legacyConfig := filepath.Join(base, ".config", "heikou", "config.json")
	legacyState := filepath.Join(base, ".local", "state", "heikou", "state.json")
	legacyNotes := filepath.Join(base, ".local", "share", "heikou", "workstreams", "w1", "notes.md")
	writeFile(t, legacyConfig, `{"default_runner":"codex"}`)
	writeFile(t, legacyState, `{"version":2}`)
	writeFile(t, legacyNotes, "durable notes")

	migration, err := Migrate()
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !migration.Migrated() {
		t.Fatal("Migrate reported no relocation")
	}
	if len(migration.Home) != 3 {
		t.Fatalf("moved %d paths, want 3: %+v", len(migration.Home), migration.Home)
	}

	dir := filepath.Join(base, DirName)
	for _, relative := range []string{"config.json", "state.json", filepath.Join("workstreams", "w1", "notes.md")} {
		if _, err := os.Stat(filepath.Join(dir, relative)); err != nil {
			t.Fatalf("expected %q under the heikou home: %v", relative, err)
		}
	}
	for _, stale := range []string{legacyConfig, legacyState, filepath.Dir(filepath.Dir(legacyNotes))} {
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Fatalf("legacy path %q still present after migration", stale)
		}
	}

	wantBase := filepath.Join(base, ".local", "share", "heikou", "workstreams")
	if migration.LegacyArtifactBase != wantBase {
		t.Fatalf("LegacyArtifactBase = %q, want %q", migration.LegacyArtifactBase, wantBase)
	}

	// The emptied legacy directories are cleaned up so the old layout does not
	// linger and look like a second installation.
	for _, emptied := range []string{
		filepath.Join(base, ".config", "heikou"),
		filepath.Join(base, ".local", "state", "heikou"),
		filepath.Join(base, ".local", "share", "heikou"),
	} {
		if _, err := os.Stat(emptied); !os.IsNotExist(err) {
			t.Fatalf("emptied legacy directory %q was not removed", emptied)
		}
	}
}

// A legacy directory holding files Heikou does not own must survive, because
// removing it would delete something the user put there.
func TestMigrateKeepsLegacyDirectoryWithForeignFiles(t *testing.T) {
	base := isolate(t)
	legacyDir := filepath.Join(base, ".config", "heikou")
	writeFile(t, filepath.Join(legacyDir, "config.json"), `{}`)
	writeFile(t, filepath.Join(legacyDir, "notes-of-my-own.txt"), "keep me")

	if _, err := Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(legacyDir, "notes-of-my-own.txt")); err != nil {
		t.Fatalf("a file Heikou does not own was removed: %v", err)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	base := isolate(t)
	writeFile(t, filepath.Join(base, ".config", "heikou", "config.json"), `{"default_runner":"codex"}`)

	first, err := Migrate()
	if err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if !first.Migrated() {
		t.Fatal("first Migrate reported no relocation")
	}
	second, err := Migrate()
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if second.Migrated() {
		t.Fatalf("second Migrate relocated again: %+v", second.Home)
	}
}

func TestMigrateSkips(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, base string)
	}{
		{
			name:  "fresh install has nothing to move",
			setup: func(t *testing.T, base string) {},
		},
		{
			name: "an explicit home override is owned by its caller",
			setup: func(t *testing.T, base string) {
				writeFile(t, filepath.Join(base, ".config", "heikou", "config.json"), `{}`)
				t.Setenv(PathEnv, t.TempDir())
			},
		},
		{
			name: "an existing heikou home is never re-migrated",
			setup: func(t *testing.T, base string) {
				writeFile(t, filepath.Join(base, ".config", "heikou", "config.json"), `{}`)
				if err := os.MkdirAll(filepath.Join(base, DirName), 0o700); err != nil {
					t.Fatalf("create home: %v", err)
				}
			},
		},
		{
			name: "an individually overridden path is left alone",
			setup: func(t *testing.T, base string) {
				writeFile(t, filepath.Join(base, ".config", "heikou", "config.json"), `{}`)
				t.Setenv("HEIKOU_CONFIG", filepath.Join(t.TempDir(), "config.json"))
			},
		},
		{
			name: "a zero-byte state file carries nothing",
			setup: func(t *testing.T, base string) {
				writeFile(t, filepath.Join(base, ".local", "state", "heikou", "state.json"), "")
			},
		},
		{
			name: "an empty artifact directory carries nothing",
			setup: func(t *testing.T, base string) {
				if err := os.MkdirAll(filepath.Join(base, ".local", "share", "heikou", "workstreams"), 0o700); err != nil {
					t.Fatalf("create artifacts: %v", err)
				}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			base := isolate(t)
			testCase.setup(t, base)
			migration, err := Migrate()
			if err != nil {
				t.Fatalf("Migrate: %v", err)
			}
			if migration.Migrated() {
				t.Fatalf("Migrate relocated %+v", migration.Home)
			}
		})
	}
}

// A migration that moved no artifacts must not report a legacy artifact base,
// because the caller uses that value to decide whether to rewrite the absolute
// artifact directories recorded in state. A spurious value would rewrite paths
// that were never relocated.
func TestMigrateReportsArtifactBaseOnlyWhenArtifactsMoved(t *testing.T) {
	base := isolate(t)
	writeFile(t, filepath.Join(base, ".config", "heikou", "config.json"), `{}`)
	writeFile(t, filepath.Join(base, ".local", "state", "heikou", "state.json"), `{"version":2}`)

	migration, err := Migrate()
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(migration.Home) != 2 {
		t.Fatalf("moved %d paths, want 2: %+v", len(migration.Home), migration.Home)
	}
	if migration.LegacyArtifactBase != "" {
		t.Fatalf("LegacyArtifactBase = %q, want empty when no artifacts moved", migration.LegacyArtifactBase)
	}
}
