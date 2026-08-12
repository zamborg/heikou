package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/zamborg/heikou/internal/home"
	manageheikou "github.com/zamborg/heikou/skills/manage-heikou"
)

// claudePointer keeps one source of truth. Claude Code looks for CLAUDE.md and
// Codex looks for AGENTS.md, so the pointer exists rather than a second copy of
// the instructions that could drift from the first.
const claudePointer = `# Heikou

Read [AGENTS.md](AGENTS.md) in this directory. It is the operating contract for
maintaining Heikou state, and it applies to you in full.
`

// pilotDoc is one file installed into the Heikou home directory.
type pilotDoc struct {
	relative string
	contents string
}

func pilotDocs() []pilotDoc {
	return []pilotDoc{
		{relative: "AGENTS.md", contents: manageheikou.Agents},
		{relative: "CLAUDE.md", contents: claudePointer},
		{relative: filepath.Join("skills", "manage-heikou", "SKILL.md"), contents: manageheikou.Skill},
	}
}

func runInit(args []string) error {
	flags := newFlagSet("h init")
	force := flags.Bool("force", false, "overwrite existing instruction files")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: h init [--force]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: h init [--force]")
	}
	dir, written, err := installPilotDocs(*force)
	if err != nil {
		return err
	}
	for _, name := range written {
		fmt.Printf("wrote %s\n", filepath.Join(dir, name))
	}
	if len(written) == 0 {
		fmt.Printf("%s already has its instructions; pass --force to refresh them\n", dir)
	}

	// --force is the explicit way to ask for setup again, including a managers
	// workstream that was removed. Without it, a removed workstream stays removed.
	if *force {
		if err := forgetProvisioning(); err != nil {
			return err
		}
	}
	socket := defaultSocket()
	if _, controller, _, err := newController(socket); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), organizeTimeout)
		provisionInstallation(ctx, controller, os.Stdout)
		cancel()
	}
	fmt.Printf("\nStart a pilot by running an agent in %s:\n\n", dir)
	fmt.Printf("  cd %s && claude\n", dir)
	fmt.Printf("  cd %s && codex\n", dir)
	return nil
}

// ensurePilotDocs installs missing instruction files on any invocation, so the
// Heikou home directory is always ready for a pilot without the user having to
// know that h init exists. It never overwrites, so it costs one stat per file
// once the directory is set up.
//
// A failure here is reported but not fatal: not being able to write AGENTS.md
// is no reason to refuse to list sessions.
func ensurePilotDocs(writer io.Writer) {
	if _, _, err := installPilotDocs(false); err != nil {
		fmt.Fprintln(writer, "heikou: could not install pilot instructions:", oneLine(err.Error()))
	}
}

// installPilotDocs writes any missing instruction file into the Heikou home
// directory. Existing files are never overwritten without --force, matching how
// settings are handled: a file the user has edited is theirs.
func installPilotDocs(force bool) (string, []string, error) {
	dir, err := home.Ensure()
	if err != nil {
		return "", nil, err
	}
	var written []string
	for _, doc := range pilotDocs() {
		path := filepath.Join(dir, doc.relative)
		if !force {
			if _, err := os.Stat(path); err == nil {
				continue
			} else if !os.IsNotExist(err) {
				return "", nil, fmt.Errorf("inspect %q: %w", path, err)
			}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", nil, fmt.Errorf("create %q: %w", filepath.Dir(path), err)
		}
		if err := writePrivateFile(path, doc.contents); err != nil {
			return "", nil, err
		}
		written = append(written, doc.relative)
	}
	return dir, written, nil
}

// writePrivateFile installs one file atomically at mode 0600, matching how
// Heikou writes every other file it owns.
func writePrivateFile(path, contents string) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".heikou-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file for %q: %w", path, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect temporary file for %q: %w", path, err)
	}
	if _, err := io.WriteString(temporary, contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write %q: %w", path, err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync %q: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary file for %q: %w", path, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install %q: %w", path, err)
	}
	return nil
}
