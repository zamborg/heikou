package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zamborg/heikou/internal/format"
	"github.com/zamborg/heikou/internal/transcript"
)

// runHistory answers "what happened in this session" from what the runner
// recorded, which is the one question `h peek` cannot answer.
//
// The two verbs never merge. Peek is the pane's current frame; history is what
// happened. A caller that conflates them will describe a screenshot as a
// conversation, which is the mistake the pilot instructions exist to prevent.
func (a *app) runHistory(args []string) error {
	flags := a.newFlagSet("h history")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	last := flags.Int("last", transcript.DefaultTurns, "how many recent turns to print; 0 for every turn")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := parseAnywhere(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: h history <session> [--last N] [--json]")
	}
	if *last < 0 {
		return errors.New("--last cannot be negative")
	}
	controller, err := a.dial(*socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), organizeTimeout)
	defer cancel()
	session, err := controller.Find(ctx, flags.Arg(0))
	if err != nil {
		return err
	}

	// Zero means every turn here, because a CLI flag reads as "no limit" and
	// not as "use the library default"; the library spells that -1.
	turns := *last
	if turns == 0 {
		turns = -1
	}
	// A resumed session appends to the conversation it continued, so the file
	// is named for that conversation and not for this session's durable id.
	// Asking by the durable id reports a resumed session as having no history.
	result, err := a.transcripts.Read(transcript.Request{
		Runner:    session.Backend,
		SessionID: session.ConversationID(),
		Root:      historyRoot(session.Root, session.Record.InitialRoot),
		Last:      turns,
	})
	if err != nil {
		return err
	}

	if *jsonOutput {
		return writeJSON(a.out, result)
	}
	writeHistory(a, result)
	return nil
}

// historyRoot prefers the durable launch directory, because a pane's reported
// path follows the agent as it moves and the transcript was filed under where
// the session started.
func historyRoot(runtimeRoot, durableRoot string) string {
	if strings.TrimSpace(durableRoot) != "" {
		return durableRoot
	}
	return runtimeRoot
}

func writeHistory(a *app, result transcript.Transcript) {
	if result.Availability != transcript.Available {
		// A missing transcript is an answer, not a failure, so it goes to
		// stdout and the verb exits zero. It names the runner because "no
		// history" means something different for each one.
		fmt.Fprintf(a.out, "no transcript for %s (%s): %s\n",
			format.ShortID(result.SessionID), result.Runner, result.Reason)
		return
	}

	fmt.Fprintf(a.out, "%s · %s transcript · %s\n",
		format.ShortID(result.SessionID), result.Runner, turnRange(result))
	if result.Bounded {
		fmt.Fprintf(a.out, "warning: the transcript is larger than Heikou reads, so these are its earliest turns, not its latest\n")
	}
	if result.SkippedRecords > 0 {
		fmt.Fprintf(a.out, "warning: %s could not be read\n", pluralRecords(result.SkippedRecords))
	}
	now := time.Now()
	for _, turn := range result.Turns {
		fmt.Fprintln(a.out)
		fmt.Fprintf(a.out, "[%d] %s · %s\n", turn.Index, turn.Role, format.RelativeTime(turn.At, now))
		for _, line := range strings.Split(turn.Text, "\n") {
			fmt.Fprintf(a.out, "    %s\n", line)
		}
		if len(turn.Tools) > 0 {
			fmt.Fprintf(a.out, "    · ran %s\n", toolLine(turn.Tools))
		}
		if turn.Truncated {
			fmt.Fprintln(a.out, "    · truncated")
		}
	}
}

func turnRange(result transcript.Transcript) string {
	switch {
	case result.TotalTurns == 0:
		return "no turns recorded"
	case len(result.Turns) == result.TotalTurns:
		return fmt.Sprintf("%d turns", result.TotalTurns)
	default:
		return fmt.Sprintf("turns %d-%d of %d",
			result.Turns[0].Index, result.Turns[len(result.Turns)-1].Index, result.TotalTurns)
	}
}

func toolLine(tools []transcript.ToolUse) string {
	parts := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool.Count == 1 {
			parts = append(parts, tool.Name)
			continue
		}
		parts = append(parts, tool.Name+" ×"+strconv.Itoa(tool.Count))
	}
	return strings.Join(parts, ", ")
}

func pluralRecords(count int) string {
	if count == 1 {
		return "1 record"
	}
	return fmt.Sprintf("%d records", count)
}
