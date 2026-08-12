// Package home resolves the single directory that holds every Heikou file:
// settings, durable application state, workstream artifacts, and the agent
// instructions a manager session reads.
//
// Heikou previously spread those across three XDG locations. One directory is
// easier to explain, easier to back up, and — because a manager agent runs
// rooted in it — the difference between an agent that can see its own working
// material and one that cannot.
package home

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// PathEnv overrides the Heikou home directory.
	PathEnv = "HEIKOU_HOME"
	// DirName is created under the user's home directory by default.
	DirName = ".heikou"
)

// Dir resolves the Heikou home directory without creating it.
func Dir() (string, error) {
	if value := strings.TrimSpace(os.Getenv(PathEnv)); value != "" {
		path, err := filepath.Abs(value)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", PathEnv, err)
		}
		return path, nil
	}
	base, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(base, DirName), nil
}

// Ensure resolves the Heikou home directory and creates it privately.
func Ensure() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create heikou home %q: %w", dir, err)
	}
	return dir, nil
}

// Path joins elements onto the Heikou home directory.
func Path(elements ...string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{dir}, elements...)...), nil
}

// Moved records one relocated path.
type Moved struct {
	From string
	To   string
}

// LegacyArtifactBase is reported when a migration relocated workstream
// artifacts, so the caller can rebase the absolute artifact directories stored
// in state. It is empty when no artifact directory moved.
type Migration struct {
	Home               []Moved
	LegacyArtifactBase string
}

// Migrated reports whether anything moved.
func (m Migration) Migrated() bool { return len(m.Home) > 0 }

// legacyLayout describes one pre-0.4 XDG location.
type legacyLayout struct {
	envOverride string
	xdgBase     string
	fallback    []string
	source      []string
	target      string
}

var legacyLayouts = []legacyLayout{
	{
		envOverride: "HEIKOU_CONFIG",
		xdgBase:     "XDG_CONFIG_HOME",
		fallback:    []string{".config"},
		source:      []string{"heikou", "config.json"},
		target:      "config.json",
	},
	{
		envOverride: "HEIKOU_STATE",
		xdgBase:     "XDG_STATE_HOME",
		fallback:    []string{".local", "state"},
		source:      []string{"heikou", "state.json"},
		target:      "state.json",
	},
	{
		envOverride: "HEIKOU_DATA",
		xdgBase:     "XDG_DATA_HOME",
		fallback:    []string{".local", "share"},
		source:      []string{"heikou", "workstreams"},
		target:      "workstreams",
	},
}

// Migrate relocates a pre-0.4 XDG layout into the Heikou home directory.
//
// It runs only when the home directory does not exist yet, so it happens at
// most once and can never race a live installation. An explicit HEIKOU_HOME
// suppresses it entirely, and any individual path override is left alone: a
// caller that chose a location owns it.
//
// Migration fails closed. A partial move reports what already succeeded so the
// user can finish it deliberately rather than run against a split installation.
func Migrate() (Migration, error) {
	if strings.TrimSpace(os.Getenv(PathEnv)) != "" {
		return Migration{}, nil
	}
	dir, err := Dir()
	if err != nil {
		return Migration{}, err
	}
	if _, err := os.Stat(dir); err == nil {
		return Migration{}, nil
	} else if !os.IsNotExist(err) {
		return Migration{}, fmt.Errorf("inspect heikou home %q: %w", dir, err)
	}

	base, err := os.UserHomeDir()
	if err != nil {
		return Migration{}, fmt.Errorf("locate home directory: %w", err)
	}

	type pending struct {
		from    string
		to      string
		isBase  bool
		present bool
	}
	var found []pending
	for _, layout := range legacyLayouts {
		if strings.TrimSpace(os.Getenv(layout.envOverride)) != "" {
			continue
		}
		root := strings.TrimSpace(os.Getenv(layout.xdgBase))
		if root == "" {
			root = filepath.Join(append([]string{base}, layout.fallback...)...)
		}
		from := filepath.Join(append([]string{root}, layout.source...)...)
		info, err := os.Stat(from)
		if err != nil {
			continue
		}
		// An empty artifact directory or a zero-byte state file carries nothing
		// worth migrating, and moving it would create a home directory for an
		// installation that was never really used.
		if !info.IsDir() && info.Size() == 0 {
			continue
		}
		if info.IsDir() {
			entries, err := os.ReadDir(from)
			if err != nil || len(entries) == 0 {
				continue
			}
		}
		found = append(found, pending{
			from: from, to: filepath.Join(dir, layout.target),
			isBase: layout.target == "workstreams", present: true,
		})
	}
	if len(found) == 0 {
		return Migration{}, nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Migration{}, fmt.Errorf("create heikou home %q: %w", dir, err)
	}
	result := Migration{}
	for _, item := range found {
		if err := os.Rename(item.from, item.to); err != nil {
			return result, fmt.Errorf(
				"move %q to %q: %w (heikou now uses one directory; move the remaining files by hand and rerun)",
				item.from, item.to, err,
			)
		}
		result.Home = append(result.Home, Moved{From: item.from, To: item.to})
		if item.isBase {
			result.LegacyArtifactBase = item.from
		}
		// The legacy "heikou" directory is empty once its contents move. Remove
		// exactly that one level and only when empty: os.Remove refuses a
		// non-empty directory, so anything a user kept alongside Heikou's files
		// keeps the directory alive rather than being deleted.
		_ = os.Remove(filepath.Dir(item.from))
	}
	return result, nil
}
