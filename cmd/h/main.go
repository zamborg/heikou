package main

import (
	"context"
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
	"github.com/zamborg/heikou/internal/runner"
	"github.com/zamborg/heikou/internal/supervisor"
	"github.com/zamborg/heikou/internal/ui"
	"github.com/zamborg/heikou/internal/workstream"
)

var version = "0.3.1"

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

	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "heikou:", oneLine(err.Error()))
		os.Exit(1)
	}
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

	program := tea.NewProgram(ui.New(controller, absRoot, backend, configStore, settings))
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("run dashboard: %w", err)
	}
	return nil
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
		Backend: backend, Prompt: prompt, Root: *root, Command: settings.Command(backend), WorkstreamID: workstreamID,
	})
	if err != nil {
		return err
	}
	name := "h-" + session.ID
	if session.Runtime != nil {
		name = session.Runtime.Name
	}
	fmt.Printf("started %s %s (%s)\n", session.Backend, shortID(session.ID), name)
	return nil
}

func runList(args []string) error {
	flags := newFlagSet("h list")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
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
	if len(snapshot.Sessions) == 0 && len(snapshot.Orphans) == 0 {
		fmt.Println("no heikou sessions")
		return nil
	}
	writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tRUNNER\tSTATE\tWORKSTREAM\tRUNTIME\tROOT\tTASK")
	all := append(append([]control.Session(nil), snapshot.Sessions...), snapshot.Orphans...)
	for _, session := range all {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			shortID(session.ID), session.Backend, cliStatus(session), sessionGroup(snapshot, session),
			formatDuration(session.RuntimeDuration(time.Now())), oneLine(compactPath(session.Root)), oneLine(session.DisplayMessage()))
	}
	return writer.Flush()
}

func runSend(args []string) error {
	flags := newFlagSet("h send")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
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
	return nil
}

func printHelp(writer io.Writer) {
	fmt.Fprintln(writer, `heikou — a fast dashboard for parallel native coding agents

Usage:
  h [--runner codex|claude|no-agent] [-C DIR]
                                        open the dashboard
  h spawn [-r RUNNER] [-C DIR] [-w WORKSTREAM] LABEL
                                        start a session without the dashboard
  h list                               list sessions
  h send ID MESSAGE                    send a follow-up through tmux
  h attach ID                          enter the native agent terminal
  h stop ID                            stop runtime; keep the durable record
  h doctor                             check local dependencies

Dashboard:
  Type + Enter      start a new session (default binding)
  Type + Tab        send to the selected live session (default binding)
  Empty Tab         switch the new-session runner (default binding)
  Empty Shift-Tab   cycle workstream roots (default binding)
  F1 / Empty ?      open scrollable help and the noun glossary
  Ctrl-S / F2       open settings (e edits JSON, r reloads)
  F3                open the expandable workstream/session organizer
  Up / Down         select a workstream or session
  Empty Enter       collapse a workstream or attach a session
  Organizer m       mark a session; Enter/m on a workstream moves it
  Organizer u/Space use a workstream or select a session and return
  Ctrl-b d          detach the native terminal back to heikou
  Ctrl-\            alternate one-chord detach shortcut
  Ctrl-X twice      stop runtime; repeat once pane-free to delete record
  Esc               clear the composer, then quit

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
	return manager, control.New(manager, stateStore, socket), stateStore, nil
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

func cliStatus(session control.Session) string {
	if session.Status == control.StatusExited {
		if code, ok := session.ExitCode(); ok && code != 0 {
			return fmt.Sprintf("failed(%d)", code)
		}
	}
	return string(session.Status)
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
