package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/format"
)

// organizeTimeout bounds one durable organization action. These commands touch
// the state file and, for adopt, one tmux lookup; none of them wait on an agent.
const organizeTimeout = 8 * time.Second

// parseAnywhere accepts flags before or after positional arguments. Every
// command that takes a positional parses through it, so the CLI reads the same
// way everywhere — an agent composing a line has no per-verb rule to remember.
//
// Go's flag package stops parsing at the first positional, so
// `h ws create "API work" -C ~/proj` would silently fold "-C ~/proj" into the
// workstream name rather than setting its root, and `h spawn "task" -r claude`
// would launch the default runner while reporting the prompt it was given.
// These commands are written by people and by agents composing a line in
// natural order, and a silent wrong result is far worse here than a parse
// error.
//
// The cost is that a positional which genuinely begins with a dash must follow
// an explicit `--`. That trade is deliberate: an unknown leading-dash token
// fails loudly and names the fix, where the old behaviour quietly sent the
// wrong message or started the wrong runner.
func parseAnywhere(flags *flag.FlagSet, args []string) error {
	var options, positional []string
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--" {
			positional = append(positional, args[index+1:]...)
			break
		}
		if len(argument) < 2 || argument[0] != '-' {
			positional = append(positional, argument)
			continue
		}
		options = append(options, argument)
		name := strings.TrimLeft(argument, "-")
		if strings.Contains(name, "=") {
			continue
		}
		definition := flags.Lookup(name)
		if definition == nil {
			continue
		}
		// A bool flag consumes no value; anything else takes the next argument.
		if boolean, ok := definition.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			continue
		}
		if index+1 < len(args) {
			index++
			options = append(options, args[index])
		}
	}
	// The explicit terminator is always re-emitted, so a positional that begins
	// with a dash — a session title, say — is never re-read as a flag.
	return flags.Parse(append(append(options, "--"), positional...))
}

// resolveWorkstreamID resolves a workstream name or id prefix through the same
// matcher `h spawn -w` uses, so every surface accepts the same shorthand.
func resolveWorkstreamID(ctx context.Context, controller *control.Controller, query string) (string, error) {
	snapshot, err := controller.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	return resolveWorkstream(snapshot, query)
}

func runWorkstreamCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: h ws <list|create|rename|reorder|archive|root>")
	}
	switch args[0] {
	case "list", "ls":
		return runWorkstreamList(args[1:])
	case "create", "new":
		return runWorkstreamCreate(args[1:])
	case "rename":
		return runWorkstreamRename(args[1:])
	case "reorder":
		return runWorkstreamReorder(args[1:])
	case "archive":
		return runWorkstreamArchive(args[1:])
	case "root":
		return runWorkstreamRoot(args[1:])
	default:
		return fmt.Errorf("unknown ws command %q; run h help", args[0])
	}
}

func runWorkstreamList(args []string) error {
	flags := newFlagSet("h ws list")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	jsonOutput := flags.Bool("json", false, "write a machine-readable list")
	if err := parseAnywhere(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: h ws list [--json]")
	}
	_, controller, _, err := newController(*socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), organizeTimeout)
	defer cancel()
	snapshot, err := controller.Snapshot(ctx)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(os.Stdout, newCLISnapshot(snapshot).Workstreams)
	}
	if len(snapshot.Workstreams) == 0 {
		fmt.Println("no workstreams; create one with h ws create NAME -C DIR")
		return nil
	}
	counts := make(map[string]int, len(snapshot.Workstreams))
	for _, session := range snapshot.Sessions {
		if session.WorkstreamID != "" {
			counts[session.WorkstreamID]++
		}
	}
	writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tNAME\tSESSIONS\tROOTS")
	for _, item := range snapshot.Workstreams {
		fmt.Fprintf(writer, "%s\t%s\t%d\t%s\n",
			format.ShortID(item.ID), format.OneLine(item.Name), counts[item.ID], strings.Join(item.Roots, ", "))
	}
	return writer.Flush()
}

func runWorkstreamCreate(args []string) error {
	flags := newFlagSet("h ws create")
	root := flags.String("root", mustWorkingDirectory(), "first registered root")
	flags.StringVar(root, "C", *root, "first registered root")
	description := flags.String("description", "", "optional description")
	flags.StringVar(description, "d", *description, "optional description")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := parseAnywhere(flags, args); err != nil {
		return err
	}
	name := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if name == "" {
		return errors.New("usage: h ws create [-C dir] [-d description] <name>")
	}
	_, controller, _, err := newController(*socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), organizeTimeout)
	defer cancel()
	item, err := controller.CreateWorkstream(ctx, name, *description, []string{*root})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(os.Stdout, map[string]any{
			"status": "created", "workstream_id": item.ID, "name": item.Name,
			"roots": item.Roots, "artifact_dir": item.ArtifactDir,
		})
	}
	fmt.Printf("created workstream %s (%s) with root %s\n", item.Name, format.ShortID(item.ID), *root)
	fmt.Printf("notes and artifacts: %s\n", item.ArtifactDir)
	return nil
}

func runWorkstreamRename(args []string) error {
	flags := newFlagSet("h ws rename")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := parseAnywhere(flags, args); err != nil {
		return err
	}
	if flags.NArg() < 2 {
		return errors.New("usage: h ws rename <workstream> <new-name>")
	}
	_, controller, _, err := newController(*socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), organizeTimeout)
	defer cancel()
	id, err := resolveWorkstreamID(ctx, controller, flags.Arg(0))
	if err != nil {
		return err
	}
	name := strings.TrimSpace(strings.Join(flags.Args()[1:], " "))
	if err := controller.RenameWorkstream(ctx, id, name); err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(os.Stdout, map[string]any{
			"status": "renamed", "workstream_id": id, "name": name,
		})
	}
	fmt.Printf("renamed %s to %s\n", format.ShortID(id), name)
	return nil
}

func runWorkstreamReorder(args []string) error {
	flags := newFlagSet("h ws reorder")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	up := flags.Bool("up", false, "move one position earlier")
	down := flags.Bool("down", false, "move one position later")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := parseAnywhere(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 1 || *up == *down {
		return errors.New("usage: h ws reorder <workstream> --up|--down")
	}
	_, controller, _, err := newController(*socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), organizeTimeout)
	defer cancel()
	id, err := resolveWorkstreamID(ctx, controller, flags.Arg(0))
	if err != nil {
		return err
	}
	delta := 1
	if *up {
		delta = -1
	}
	moved, err := controller.ReorderWorkstream(ctx, id, delta)
	if err != nil {
		return err
	}
	status := "moved"
	if !moved {
		status = "already at the edge"
	}
	if *jsonOutput {
		return writeJSON(os.Stdout, map[string]any{
			"status": "reordered", "workstream_id": id, "moved": moved,
		})
	}
	fmt.Printf("%s %s\n", format.ShortID(id), status)
	return nil
}

func runWorkstreamArchive(args []string) error {
	flags := newFlagSet("h ws archive")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	confirm := flags.Bool("yes", false, "confirm archiving")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := parseAnywhere(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: h ws archive <workstream> --yes")
	}
	if !*confirm {
		return errors.New("archiving moves every member session to Ungrouped; pass --yes to confirm")
	}
	_, controller, _, err := newController(*socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), organizeTimeout)
	defer cancel()
	id, err := resolveWorkstreamID(ctx, controller, flags.Arg(0))
	if err != nil {
		return err
	}
	if err := controller.ArchiveWorkstream(ctx, id); err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(os.Stdout, map[string]any{"status": "archived", "workstream_id": id})
	}
	fmt.Printf("archived %s; its sessions are now Ungrouped\n", format.ShortID(id))
	return nil
}

func runWorkstreamRoot(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: h ws root <add|set|rm> <workstream> <dir>")
	}
	action := args[0]
	flags := newFlagSet("h ws root " + action)
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := parseAnywhere(flags, args[1:]); err != nil {
		return err
	}

	want := 2
	if action == "set" {
		want = 3
	}
	if flags.NArg() != want {
		switch action {
		case "add":
			return errors.New("usage: h ws root add <workstream> <dir>")
		case "set":
			return errors.New("usage: h ws root set <workstream> <current-dir> <new-dir>")
		case "rm", "remove":
			return errors.New("usage: h ws root rm <workstream> <dir>")
		default:
			return fmt.Errorf("unknown ws root command %q; want add, set, or rm", action)
		}
	}

	_, controller, _, err := newController(*socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), organizeTimeout)
	defer cancel()
	id, err := resolveWorkstreamID(ctx, controller, flags.Arg(0))
	if err != nil {
		return err
	}

	var status string
	switch action {
	case "add":
		err, status = controller.AddRoot(ctx, id, flags.Arg(1)), "added"
	case "set":
		err, status = controller.ReplaceRoot(ctx, id, flags.Arg(1), flags.Arg(2)), "replaced"
	case "rm", "remove":
		err, status = controller.RemoveRoot(ctx, id, flags.Arg(1)), "removed"
	default:
		return fmt.Errorf("unknown ws root command %q; want add, set, or rm", action)
	}
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(os.Stdout, map[string]any{
			"status": status, "workstream_id": id, "root": flags.Arg(1),
		})
	}
	fmt.Printf("%s root on %s\n", status, format.ShortID(id))
	return nil
}

func runTitle(args []string) error {
	flags := newFlagSet("h title")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	clear := flags.Bool("clear", false, "clear the durable title")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := parseAnywhere(flags, args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return errors.New("usage: h title <session> <title>   |   h title <session> --clear")
	}
	title := strings.TrimSpace(strings.Join(flags.Args()[1:], " "))
	// Clearing is explicit. A bare `h title ID` would otherwise silently erase a
	// title that took thought to write.
	if *clear && title != "" {
		return errors.New("pass either a new title or --clear, not both")
	}
	if !*clear && title == "" {
		return errors.New("usage: h title <session> <title>   |   h title <session> --clear")
	}

	_, controller, _, err := newController(*socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), organizeTimeout)
	defer cancel()
	session, err := controller.Find(ctx, flags.Arg(0))
	if err != nil {
		return err
	}
	if err := controller.SetSessionTitle(ctx, session.ID, title); err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(os.Stdout, map[string]any{
			"status": "titled", "session_id": session.ID, "title": title,
		})
	}
	if title == "" {
		fmt.Printf("cleared the title on %s\n", format.ShortID(session.ID))
		return nil
	}
	fmt.Printf("titled %s: %s\n", format.ShortID(session.ID), title)
	return nil
}

func runMove(args []string) error {
	flags := newFlagSet("h move")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	target := flags.String("workstream", "", "destination workstream name or id")
	flags.StringVar(target, "w", *target, "destination workstream name or id")
	ungrouped := flags.Bool("ungrouped", false, "remove the session from its workstream")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := parseAnywhere(flags, args); err != nil {
		return err
	}
	// Requiring an explicit destination keeps a mistyped flag from silently
	// ungrouping a session instead of moving it.
	if flags.NArg() != 1 || (strings.TrimSpace(*target) == "") == !*ungrouped {
		return errors.New("usage: h move <session> --workstream NAME|--ungrouped")
	}
	_, controller, _, err := newController(*socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), organizeTimeout)
	defer cancel()
	session, err := controller.Find(ctx, flags.Arg(0))
	if err != nil {
		return err
	}
	workstreamID := ""
	if !*ungrouped {
		if workstreamID, err = resolveWorkstreamID(ctx, controller, *target); err != nil {
			return err
		}
	}
	if err := controller.MoveSession(ctx, session.ID, workstreamID); err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(os.Stdout, map[string]any{
			"status": "moved", "session_id": session.ID, "workstream_id": workstreamID,
		})
	}
	if workstreamID == "" {
		fmt.Printf("moved %s to Ungrouped\n", format.ShortID(session.ID))
		return nil
	}
	fmt.Printf("moved %s to %s\n", format.ShortID(session.ID), format.ShortID(workstreamID))
	return nil
}

func runAdopt(args []string) error {
	flags := newFlagSet("h adopt")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	target := flags.String("workstream", "", "destination workstream name or id")
	flags.StringVar(target, "w", *target, "destination workstream name or id")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := parseAnywhere(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: h adopt <orphaned-session> [-w workstream]")
	}
	_, controller, _, err := newController(*socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), organizeTimeout)
	defer cancel()
	session, err := controller.Find(ctx, flags.Arg(0))
	if err != nil {
		return err
	}
	workstreamID := ""
	if strings.TrimSpace(*target) != "" {
		if workstreamID, err = resolveWorkstreamID(ctx, controller, *target); err != nil {
			return err
		}
	}
	adopted, err := controller.AdoptSession(ctx, session.ID, workstreamID)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(os.Stdout, map[string]any{
			"status": "adopted", "session_id": adopted.ID, "workstream_id": workstreamID,
		})
	}
	fmt.Printf("adopted %s into durable state\n", format.ShortID(adopted.ID))
	return nil
}

func runDelete(args []string) error {
	flags := newFlagSet("h delete")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	confirm := flags.Bool("yes", false, "confirm deleting the durable record")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := parseAnywhere(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: h delete <session> --yes")
	}
	if !*confirm {
		return errors.New("deleting discards the durable session record and its history; pass --yes to confirm")
	}
	_, controller, _, err := newController(*socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), organizeTimeout)
	defer cancel()
	session, err := controller.Find(ctx, flags.Arg(0))
	if err != nil {
		return err
	}
	if err := controller.DeleteSession(ctx, session.ID); err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(os.Stdout, map[string]any{"status": "deleted", "session_id": session.ID})
	}
	fmt.Printf("deleted the durable record for %s\n", format.ShortID(session.ID))
	return nil
}

func runPeek(args []string) error {
	flags := newFlagSet("h peek")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	lines := flags.Int("lines", 80, "how many captured lines to request")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := parseAnywhere(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: h peek <session> [--lines N]")
	}
	_, controller, _, err := newController(*socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), organizeTimeout)
	defer cancel()
	session, err := controller.Find(ctx, flags.Arg(0))
	if err != nil {
		return err
	}
	capture, err := controller.Capture(ctx, session.ID, *lines)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(os.Stdout, map[string]any{
			"session_id": session.ID,
			"state":      cliStatus(session),
			"capture":    capture,
			// A full-screen runner draws on the alternate screen, which keeps no
			// scrollback. What comes back is the pane's current frame, not a
			// transcript, and callers must not describe it as conversation history.
			"capture_is_current_frame_only": true,
		})
	}
	fmt.Println(capture)
	return nil
}
