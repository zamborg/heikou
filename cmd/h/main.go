package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/zamborg/heikou/internal/config"
	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/home"
	"github.com/zamborg/heikou/internal/runner"
	"github.com/zamborg/heikou/internal/supervisor"
	"github.com/zamborg/heikou/internal/ui"
	"github.com/zamborg/heikou/internal/workstream"
	learnheikou "github.com/zamborg/heikou/skills/learn-heikou"
)

var version = "0.3.6"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "__agent" {
		if len(os.Args) != 6 {
			fmt.Fprintln(os.Stderr, "heikou: invalid internal runner invocation")
			os.Exit(2)
		}
		if err := runner.ExecEncoded(os.Args[2], os.Args[3], os.Args[4], os.Args[5]); err != nil {
			fmt.Fprintln(os.Stderr, "heikou:", err)
			os.Exit(127)
		}
		return
	}

	// Relocation runs at the process entry point rather than inside run so the
	// test surface never migrates a developer's real installation.
	if err := migrateHome(os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "heikou:", oneLine(err.Error()))
		os.Exit(1)
	}

	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "heikou:", oneLine(err.Error()))
		os.Exit(1)
	}
}

// migrateHome performs the one-time relocation from the pre-0.4 XDG layout into
// the Heikou home directory. It runs before any command so that no surface ever
// reads a half-moved installation.
func migrateHome(writer io.Writer) error {
	migration, err := home.Migrate()
	if err != nil {
		return err
	}
	if !migration.Migrated() {
		return nil
	}
	for _, moved := range migration.Home {
		fmt.Fprintf(writer, "heikou: moved %s to %s\n", moved.From, moved.To)
	}
	if migration.LegacyArtifactBase == "" {
		return nil
	}
	store, err := workstream.DefaultStore()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rebased, err := store.RebaseArtifacts(ctx, migration.LegacyArtifactBase)
	if err != nil {
		return fmt.Errorf("repoint workstream artifact directories: %w", err)
	}
	if rebased == 1 {
		fmt.Fprintln(writer, "heikou: repointed 1 workstream artifact directory")
	} else if rebased > 1 {
		fmt.Fprintf(writer, "heikou: repointed %d workstream artifact directories\n", rebased)
	}
	return nil
}

func run(args []string) error {
	return runWithGlobalOutput(args, os.Stdout)
}

func runWithGlobalOutput(args []string, writer io.Writer) error {
	if routeGlobalCommand(args, writer) {
		return nil
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return runDashboard(args)
	}
	switch args[0] {
	case "doctor":
		return runDoctor(args[1:])
	case "quickstart":
		return runQuickstart(args[1:])
	case "list", "ls":
		return runList(args[1:])
	case "spawn", "new":
		return runSpawn(args[1:])
	case "send":
		return runSend(args[1:])
	case "attach":
		return runAttach(args[1:])
	case "stop", "rm":
		return runStop(args[1:])
	default:
		return fmt.Errorf("unknown command %q; run h help", args[0])
	}
}

func routeGlobalCommand(args []string, writer io.Writer) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "help", "-h", "--help":
		printHelp(writer)
		return true
	case "version", "--version":
		fmt.Fprintln(writer, "heikou", version)
		return true
	default:
		return false
	}
}

func runDashboard(args []string) error {
	return runDashboardSelected(args, "")
}

func runDashboardSelected(args []string, selectedSessionID string) error {
	configStore, settings, err := loadSettings()
	if err != nil {
		return err
	}
	flags := newFlagSet("h")
	root := flags.String("root", mustWorkingDirectory(), "root directory for newly spawned agents")
	flags.StringVar(root, "C", *root, "root directory for newly spawned agents")
	runnerValue := flags.String("runner", string(settings.DefaultRunner), "default runner: codex, claude, or no-agent")
	flags.StringVar(runnerValue, "r", *runnerValue, "default runner: codex, claude, or no-agent")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	flags.Usage = func() { printHelp(flags.Output()) }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	backend, err := heikou.ParseBackend(*runnerValue)
	if err != nil {
		return err
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return fmt.Errorf("resolve root: %w", err)
	}
	rootInfo, err := os.Stat(absRoot)
	if err != nil {
		return fmt.Errorf("open root %q: %w", absRoot, err)
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("root %q is not a directory", absRoot)
	}
	manager, controller, _, err := newController(*socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := manager.Bootstrap(ctx); err != nil {
		return err
	}

	program := tea.NewProgram(ui.NewWithSelectedSession(controller, absRoot, backend, configStore, settings, selectedSessionID))
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("run dashboard: %w", err)
	}
	return nil
}

func runQuickstart(args []string) error {
	flags := newFlagSet("h quickstart")
	root := flags.String("root", mustWorkingDirectory(), "project root for the guided session")
	flags.StringVar(root, "C", *root, "project root for the guided session")
	runnerValue := flags.String("runner", "", "guide runner: claude or codex (default: prefer claude)")
	flags.StringVar(runnerValue, "r", *runnerValue, "guide runner: claude or codex (default: prefer claude)")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: h quickstart [-r claude|codex] [-C DIR]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: h quickstart [-r claude|codex] [-C dir]")
	}
	_, settings, err := loadSettings()
	if err != nil {
		return err
	}
	backend, err := quickstartBackend(*runnerValue, settings)
	if err != nil {
		return err
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return fmt.Errorf("resolve root: %w", err)
	}
	_, controller, _, err := newController(*socket)
	if err != nil {
		return err
	}
	startContext, cancelStart := context.WithTimeout(context.Background(), 8*time.Second)
	session, err := controller.Start(startContext, control.StartRequest{
		Backend: backend,
		Prompt:  quickstartPrompt(),
		Root:    absRoot,
	})
	cancelStart()
	if err != nil {
		return err
	}

	fmt.Printf("started guided %s session %s\n", backend, shortID(session.ID))
	fmt.Fprintln(os.Stderr, "attaching now · your first lesson is Ctrl-b, release, then d")
	attachContext, cancelAttach := context.WithTimeout(context.Background(), 5*time.Second)
	command, err := controller.AttachCommand(attachContext, session.ID)
	cancelAttach()
	if err != nil {
		return fmt.Errorf("attach guide %s: %w (the session is still available in h)", shortID(session.ID), err)
	}
	if err := command.Run(); err != nil {
		return fmt.Errorf("attach guide %s: %w (the session is still available in h)", shortID(session.ID), err)
	}

	fmt.Fprintln(os.Stderr, "detached · opening heikou with the guide selected")
	return runDashboardSelected([]string{
		"--runner", string(backend),
		"--root", absRoot,
		"--socket", *socket,
	}, session.ID)
}

func quickstartBackend(value string, settings config.Config) (heikou.Backend, error) {
	if strings.TrimSpace(value) != "" {
		backend, err := heikou.ParseBackend(value)
		if err != nil {
			return "", err
		}
		if backend == heikou.BackendNoAgent {
			return "", errors.New("quickstart requires the claude or codex runner")
		}
		if _, err := runner.ResolveCommand(backend, settings.Command(backend)); err != nil {
			return "", err
		}
		return backend, nil
	}

	var failures []string
	for _, backend := range []heikou.Backend{heikou.BackendClaude, heikou.BackendCodex} {
		if _, err := runner.ResolveCommand(backend, settings.Command(backend)); err == nil {
			return backend, nil
		} else {
			failures = append(failures, err.Error())
		}
	}
	return "", fmt.Errorf("quickstart requires claude or codex: %s", strings.Join(failures, "; "))
}

func quickstartPrompt() string {
	return "Guide me through my first Heikou workstream and session. You are running inside the guided session created by h quickstart. Follow the embedded skill below, begin with its in-session path, teach one action at a time, and wait for me after each action.\n\n" + learnheikou.Instructions
}

func runSpawn(args []string) error {
	_, settings, err := loadSettings()
	if err != nil {
		return err
	}
	flags := newFlagSet("h spawn")
	root := flags.String("root", mustWorkingDirectory(), "agent working directory")
	flags.StringVar(root, "C", *root, "agent working directory")
	runnerValue := flags.String("runner", string(settings.DefaultRunner), "runner: codex, claude, or no-agent")
	flags.StringVar(runnerValue, "r", *runnerValue, "runner: codex, claude, or no-agent")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	workstreamQuery := flags.String("workstream", "", "workstream name or id (default: Ungrouped)")
	flags.StringVar(workstreamQuery, "w", *workstreamQuery, "workstream name or id (default: Ungrouped)")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := flags.Parse(args); err != nil {
		return err
	}
	prompt := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if prompt == "" {
		return errors.New("usage: h spawn [-r codex|claude|no-agent] [-C dir] <task-or-label>")
	}
	backend, err := heikou.ParseBackend(*runnerValue)
	if err != nil {
		return err
	}
	_, controller, _, err := newController(*socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	workstreamID := ""
	if strings.TrimSpace(*workstreamQuery) != "" {
		snapshot, snapshotErr := controller.Snapshot(ctx)
		if snapshotErr != nil {
			return snapshotErr
		}
		workstreamID, err = resolveWorkstream(snapshot, *workstreamQuery)
		if err != nil {
			return err
		}
	}
	session, err := controller.Start(ctx, control.StartRequest{
		Backend: backend, Prompt: prompt, Root: *root, WorkstreamID: workstreamID,
	})
	if err != nil {
		return err
	}
	name := "h-" + session.ID
	if session.Runtime != nil {
		name = session.Runtime.Name
	}
	if *jsonOutput {
		return writeJSON(os.Stdout, map[string]any{
			"id": session.ID, "runner": session.Backend, "state": cliStatus(session),
			"workstream_id": session.WorkstreamID, "runtime_name": name, "root": session.Root,
		})
	}
	fmt.Printf("started %s %s (%s)\n", session.Backend, shortID(session.ID), name)
	return nil
}

func runList(args []string) error {
	flags := newFlagSet("h list")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	jsonOutput := flags.Bool("json", false, "write a machine-readable snapshot")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: h list")
	}
	_, controller, _, err := newController(*socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snapshot, err := controller.Snapshot(ctx)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(os.Stdout, newCLISnapshot(snapshot))
	}
	if len(snapshot.Sessions) == 0 && len(snapshot.Orphans) == 0 {
		fmt.Println("no heikou sessions")
		return nil
	}
	writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tRUNNER\tSTATE\tWORKSTREAM\tRUNTIME\tROOT\tSESSION")
	all := append(append([]control.Session(nil), snapshot.Sessions...), snapshot.Orphans...)
	for _, session := range all {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			shortID(session.ID), session.Backend, cliStatus(session), sessionGroup(snapshot, session),
			formatDuration(session.RuntimeDuration(time.Now())), oneLine(compactPath(session.Root)), cliSessionSummary(session))
	}
	return writer.Flush()
}

func runSend(args []string) error {
	flags := newFlagSet("h send")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() < 2 {
		return errors.New("usage: h send <session-id> <message>")
	}
	_, controller, _, err := newController(*socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	session, err := controller.Find(ctx, flags.Arg(0))
	if err != nil {
		return err
	}
	if err := controller.Send(ctx, session.ID, strings.Join(flags.Args()[1:], " ")); err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(os.Stdout, map[string]any{"session_id": session.ID, "status": "sent"})
	}
	fmt.Println("sent to", shortID(session.ID))
	return nil
}

func runAttach(args []string) error {
	flags := newFlagSet("h attach")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: h attach <session-id>")
	}
	_, controller, _, err := newController(*socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := controller.Find(ctx, flags.Arg(0))
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "detach back with Ctrl-\\ or Ctrl-b d")
	command, err := controller.AttachCommand(ctx, session.ID)
	if err != nil {
		return err
	}
	return command.Run()
}

func runStop(args []string) error {
	flags := newFlagSet("h stop")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: h stop <session-id>")
	}
	_, controller, _, err := newController(*socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	session, err := controller.Find(ctx, flags.Arg(0))
	if err != nil {
		return err
	}
	if err := controller.Stop(ctx, session.ID); err != nil {
		return err
	}
	fmt.Println("stopped", shortID(session.ID))
	return nil
}

func runDoctor(args []string) error {
	configStore, settings, err := loadSettings()
	if err != nil {
		return err
	}
	flags := newFlagSet("h doctor")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: h doctor")
	}
	stateStore, err := workstream.DefaultStore()
	if err != nil {
		return err
	}
	checks := []struct {
		name     string
		binary   string
		backend  heikou.Backend
		version  []string
		required bool
		runner   bool
	}{
		{name: "tmux", binary: "tmux", version: []string{"-V"}, required: true},
		{name: "codex", backend: heikou.BackendCodex, version: []string{"--version"}, runner: true},
		{name: "claude", backend: heikou.BackendClaude, version: []string{"--version"}, runner: true},
	}
	failed := false
	runnersFound := 0
	for _, check := range checks {
		path := ""
		var command []string
		var err error
		if check.runner {
			command, err = runner.ResolveCommand(check.backend, settings.Command(check.backend))
			if err == nil {
				path = command[0]
			}
		} else {
			path, err = exec.LookPath(check.binary)
			command = []string{path}
		}
		if err != nil {
			label := "optional"
			if check.required {
				label, failed = "required", true
			}
			requested := check.binary
			if check.runner {
				configured := settings.Command(check.backend)
				if len(configured) > 0 {
					requested = configured[0]
				}
			}
			fmt.Printf("[missing] %-7s %s (%s)\n", check.name, oneLine(requested), label)
			continue
		}
		if check.runner {
			runnersFound++
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		versionArguments := append(append([]string(nil), command[1:]...), check.version...)
		output, err := exec.CommandContext(ctx, path, versionArguments...).CombinedOutput()
		cancel()
		if err != nil {
			fmt.Printf("[warn]    %-7s %s\n", check.name, oneLine(path))
			continue
		}
		versionText := oneLine(string(output))
		if check.name == "tmux" {
			if supported, known := supportedTmuxVersion(versionText); known && !supported {
				failed = true
				fmt.Printf("[unsupported] %-7s %s · %s (need tmux 3.3+)\n", check.name, oneLine(path), versionText)
				continue
			}
		}
		fmt.Printf("[ok]      %-7s %s · %s\n", check.name, oneLine(path), versionText)
	}
	if runnersFound == 0 {
		failed = true
		fmt.Println("[missing] runner  install at least one of codex or claude")
	}
	fmt.Printf("[config]  socket  tmux -L %s\n", oneLine(*socket))
	fmt.Printf("[config]  file    %s\n", oneLine(configStore.Path))
	fmt.Printf("[state]   file    %s\n", oneLine(stateStore.Path))
	fmt.Printf("[state]   files   %s\n", oneLine(stateStore.Artifacts))
	fmt.Printf("[config]  runner  %s\n", settings.DefaultRunner)
	fmt.Printf("[config]  root    %s\n", oneLine(mustWorkingDirectory()))
	if failed {
		return errors.New("required dependencies are missing")
	}
	fmt.Println("[next]   tour    h quickstart")
	return nil
}

func printHelp(writer io.Writer) {
	fmt.Fprintln(writer, `heikou — a fast dashboard for parallel native coding agents

Usage:
  h [--runner codex|claude|no-agent] [-C DIR]
                                        open the dashboard
  h quickstart [-r claude|codex] [-C DIR]
                                        launch and attach an agent-guided tour
  h spawn [--json] [-r RUNNER] [-C DIR] [-w WORKSTREAM] LABEL
                                        start a session without the dashboard
  h list [--json]                      list sessions
  h send [--json] ID MESSAGE           send a follow-up through tmux
  h attach ID                          enter the native agent terminal
  h stop ID                            stop runtime; keep the durable record
  h doctor                             check local dependencies

Dashboard:
  Enter             send the draft where the composer prefix says it goes
  Empty Space       aim the composer at the selected live session
  Shift-Enter       insert a composer newline (Ctrl-J fallback)
  Option-←/→        move by word; Option-Delete deletes a word
  Command-←/→       move to logical line start/end
  Command-↑/↓       move to whole-draft start/end
  Tab               switch the new-session runner (default binding)
  Shift-Tab         cycle workstream roots (default binding)
  F1 / Empty ?      open scrollable help and the noun glossary
  Ctrl-S / F2       open settings (e edits JSON, r reloads)
  F3                open the expandable workstream/session organizer
  Up / Down         select a session; move draft lines when multiline
  Ctrl-G            resize snapshot/context with Up/Down; r resets
  Empty Enter       collapse a workstream or attach a session (not while replying)
  Organizer Shift-↑/↓ reorder a named workstream persistently
  Organizer r       rename a workstream or edit/clear a session title
  Organizer R       refresh selected notes and artifact context
  Organizer m       mark a session; Enter/m on a workstream moves it
  Organizer u/Space use a workstream or select a session and return
  Ctrl-b d          detach the native terminal back to heikou
  Ctrl-\            alternate one-chord detach shortcut
  Ctrl-X twice      stop runtime; repeat once pane-free to delete record
  Esc               clear the composer, leave a reply, then quit

Composer bindings are configurable in JSON and shown in settings/help.
Closing heikou never stops agents. Both h and H invoke the same binary.`)
}

func newFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	return flags
}

func defaultSocket() string {
	return envOr("HEIKOU_TMUX_SOCKET", supervisor.DefaultSocket)
}

func loadSettings() (config.Store, config.Config, error) {
	store, err := config.DefaultStore()
	if err != nil {
		return config.Store{}, config.Config{}, err
	}
	settings, err := store.Load()
	if err != nil {
		return config.Store{}, config.Config{}, err
	}
	return store, settings, nil
}

func newController(socket string) (*supervisor.Tmux, *control.Controller, workstream.FileStore, error) {
	manager, err := supervisor.New(socket)
	if err != nil {
		return nil, nil, workstream.FileStore{}, err
	}
	stateStore, err := workstream.DefaultStore()
	if err != nil {
		return nil, nil, workstream.FileStore{}, err
	}
	configStore, err := config.DefaultStore()
	if err != nil {
		return nil, nil, workstream.FileStore{}, err
	}
	resolver := control.ResolveCommandFunc(func(_ context.Context, backend heikou.Backend) ([]string, error) {
		if backend == heikou.BackendNoAgent {
			return nil, nil
		}
		settings, err := configStore.Load()
		if err != nil {
			return nil, err
		}
		return runner.ResolveCommand(backend, settings.Command(backend))
	})
	return manager, control.New(manager, stateStore, socket, control.WithCommandResolver(resolver)), stateStore, nil
}

func resolveWorkstream(snapshot control.Snapshot, query string) (string, error) {
	query = strings.TrimSpace(query)
	var matches []workstream.Workstream
	for _, item := range snapshot.Workstreams {
		if item.ID == query || strings.EqualFold(item.Name, query) {
			return item.ID, nil
		}
		if strings.HasPrefix(item.ID, query) || strings.HasPrefix(strings.ToLower(item.Name), strings.ToLower(query)) {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no active workstream matches %q", query)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("workstream query %q is ambiguous", query)
	}
	return matches[0].ID, nil
}

type cliSnapshotJSON struct {
	Revision    uint64              `json:"revision"`
	Workstreams []cliWorkstreamJSON `json:"workstreams"`
	Sessions    []cliSessionJSON    `json:"sessions"`
}

type cliWorkstreamJSON struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	ArtifactDir string   `json:"artifact_dir"`
	Roots       []string `json:"roots"`
	Revision    uint64   `json:"revision"`
}

type cliSessionJSON struct {
	ID              string         `json:"id"`
	Runner          heikou.Backend `json:"runner"`
	State           string         `json:"state"`
	Title           string         `json:"title,omitempty"`
	DisplayTitle    string         `json:"display_title"`
	InitialPrompt   string         `json:"initial_prompt"`
	LatestViaHeikou string         `json:"latest_via_heikou,omitempty"`
	WorkstreamID    string         `json:"workstream_id,omitempty"`
	Workstream      string         `json:"workstream"`
	Root            string         `json:"root"`
	Available       bool           `json:"available"`
	Alive           bool           `json:"alive"`
	Orphaned        bool           `json:"orphaned"`
	ExitCode        *int           `json:"exit_code"`
	RuntimeSeconds  int64          `json:"runtime_seconds"`
	LastActivityAt  *time.Time     `json:"last_activity_at,omitempty"`
}

func newCLISnapshot(snapshot control.Snapshot) cliSnapshotJSON {
	result := cliSnapshotJSON{
		Revision:    snapshot.Revision,
		Workstreams: make([]cliWorkstreamJSON, 0, len(snapshot.Workstreams)),
		Sessions:    make([]cliSessionJSON, 0, len(snapshot.Sessions)+len(snapshot.Orphans)),
	}
	for _, item := range snapshot.Workstreams {
		result.Workstreams = append(result.Workstreams, cliWorkstreamJSON{
			ID: item.ID, Name: item.Name, Description: item.Description,
			ArtifactDir: item.ArtifactDir, Roots: append([]string(nil), item.Roots...), Revision: item.Revision,
		})
	}
	all := append(append([]control.Session(nil), snapshot.Sessions...), snapshot.Orphans...)
	for _, session := range all {
		var exitCode *int
		if code, known := session.ExitCode(); known {
			value := code
			exitCode = &value
		}
		title := strings.TrimSpace(session.Record.Title)
		displayTitle := title
		if displayTitle == "" {
			displayTitle = oneLine(session.Prompt)
		}
		var lastActivityAt *time.Time
		if observed := session.LastActivity(); !observed.IsZero() {
			value := observed
			lastActivityAt = &value
		}
		result.Sessions = append(result.Sessions, cliSessionJSON{
			ID: session.ID, Runner: session.Backend, State: string(session.Status),
			Title: title, DisplayTitle: displayTitle, InitialPrompt: session.Prompt,
			LatestViaHeikou: session.LastUserMessage,
			WorkstreamID:    session.WorkstreamID, Workstream: sessionGroup(snapshot, session),
			Root: session.Root, Available: session.Available(), Alive: session.Alive(), Orphaned: session.Orphaned,
			ExitCode: exitCode, RuntimeSeconds: int64(session.RuntimeDuration(time.Now()).Seconds()),
			LastActivityAt: lastActivityAt,
		})
	}
	return result
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write JSON: %w", err)
	}
	return nil
}

func cliStatus(session control.Session) string {
	if session.Status == control.StatusExited {
		if code, ok := session.ExitCode(); ok {
			if code != 0 {
				return fmt.Sprintf("failed(%d)", code)
			}
		} else {
			return "exited(?)"
		}
	}
	return string(session.Status)
}

func cliSessionSummary(session control.Session) string {
	title := strings.TrimSpace(session.Record.Title)
	if title == "" {
		title = session.Prompt
	}
	title = oneLine(title)
	if latest := oneLine(session.LastUserMessage); latest != "" && latest != title {
		return title + " · latest: " + latest
	}
	return title
}

func sessionGroup(snapshot control.Snapshot, session control.Session) string {
	if session.Orphaned {
		return "Orphaned"
	}
	if session.WorkstreamID == "" {
		return "Ungrouped"
	}
	for _, item := range snapshot.Workstreams {
		if item.ID == session.WorkstreamID {
			return item.Name
		}
	}
	return "Unavailable"
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func mustWorkingDirectory() string {
	value, err := os.Getwd()
	if err != nil {
		return "."
	}
	return value
}

func shortID(id string) string {
	id = strings.ReplaceAll(id, "-", "")
	if len(id) <= 6 {
		return id
	}
	return id[:6]
}

func formatDuration(value time.Duration) string {
	if value < time.Minute {
		return fmt.Sprintf("%ds", max(0, int(value.Seconds())))
	}
	if value < time.Hour {
		return fmt.Sprintf("%dm", int(value.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(value.Hours()), int(value.Minutes())%60)
}

func compactPath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && (path == home || strings.HasPrefix(path, home+string(filepath.Separator))) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func oneLine(value string) string {
	value = ansi.Strip(value)
	value = strings.Map(func(r rune) rune {
		if !unicode.IsControl(r) || unicode.IsSpace(r) {
			return r
		}
		return -1
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func supportedTmuxVersion(value string) (supported, known bool) {
	fields := strings.Fields(value)
	if len(fields) < 2 || fields[0] != "tmux" {
		return false, false
	}
	parts := strings.SplitN(fields[1], ".", 2)
	if len(parts) != 2 {
		return false, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false, false
	}
	minorDigits := strings.TrimRightFunc(parts[1], func(r rune) bool { return r < '0' || r > '9' })
	if minorDigits == "" {
		return false, false
	}
	minor, err := strconv.Atoi(minorDigits)
	if err != nil {
		return false, false
	}
	return major > 3 || (major == 3 && minor >= 3), true
}
