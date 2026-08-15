package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/zamborg/heikou/internal/format"
	"github.com/zamborg/heikou/internal/workstream"
)

// runResume continues a session's native conversation in a new pane.
//
// This is the verb the durable conversation registration exists for. A tmux
// pane dies and the session becomes unreachable, but the runner wrote the
// conversation to disk and Heikou knows its id — so the work can be picked up
// where it stopped instead of restarted cold.
//
// It deliberately creates a new session rather than reviving the old record.
// The old record is the history of what already happened, including how it
// ended; rewriting it to look alive would destroy the one durable account of
// that. The new session records the conversation it was handed.
func (a *app) runResume(args []string) error {
	flags := a.newFlagSet("h resume")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := parseAnywhere(flags, args); err != nil {
		return err
	}
	if flags.NArg() < 2 {
		return errors.New("usage: h resume <session-id> <message>; put -- before a message that starts with a dash")
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
	if !session.Durable {
		return fmt.Errorf("session %s has no durable record, so Heikou never registered its conversation",
			format.ShortID(session.ID))
	}

	prompt := strings.Join(flags.Args()[1:], " ")
	resumed, err := controller.ResumeSession(ctx, session.ID, prompt)
	if err != nil {
		return err
	}
	conversation := workstream.Conversation{}
	if resumed.Record.Conversation != nil {
		conversation = *resumed.Record.Conversation
	}

	name := "h-" + resumed.ID
	if resumed.Runtime != nil {
		name = resumed.Runtime.Name
	}
	if *jsonOutput {
		return writeJSON(a.out, map[string]any{
			"id": resumed.ID, "runner": resumed.Backend, "state": cliStatus(resumed),
			"resumed_from": session.ID, "conversation_id": conversation.ID,
			"conversation_source": conversation.Source,
			"workstream_id":       resumed.WorkstreamID, "runtime_name": name, "root": resumed.Root,
		})
	}
	fmt.Fprintf(a.out, "resumed %s conversation %s as %s (%s)\n",
		resumed.Backend, format.ShortID(conversation.ID), format.ShortID(resumed.ID), name)
	return nil
}

// runConversation reports the native conversation id Heikou has registered for
// a session, registering it first when the runner minted it and Heikou has not
// had to ask before.
//
// It prints the source alongside the id, and that is the point of the verb: an
// id Heikou assigned is a fact, and an id Heikou matched against runner-written
// files is evidence. A reader about to act on one deserves to know which it is.
func (a *app) runConversation(args []string) error {
	flags := a.newFlagSet("h conversation")
	socket := flags.String("socket", defaultSocket(), "private tmux socket name")
	jsonOutput := flags.Bool("json", false, "write a machine-readable result")
	if err := parseAnywhere(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: h conversation <session-id> [--json]")
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

	conversation, registerErr := controller.RegisterConversation(ctx, session.ID)
	if registerErr != nil {
		// An unregistered conversation is an answer, not a failure: Codex mints
		// its own id and the record that would prove which one may be absent.
		// The verb says why, and exits zero, for the same reason h history does.
		if *jsonOutput {
			return writeJSON(a.out, map[string]any{
				"session_id": session.ID, "runner": session.Backend,
				"registered": false, "reason": format.OneLine(registerErr.Error()),
			})
		}
		fmt.Fprintf(a.out, "no conversation registered for %s (%s): %s\n",
			format.ShortID(session.ID), session.Backend, format.OneLine(registerErr.Error()))
		return nil
	}

	if *jsonOutput {
		return writeJSON(a.out, map[string]any{
			"session_id": session.ID, "runner": session.Backend, "registered": true,
			"conversation_id": conversation.ID, "conversation_source": conversation.Source,
			"recorded_at": conversation.RecordedAt,
		})
	}
	fmt.Fprintf(a.out, "%s · %s conversation %s (%s)\n",
		format.ShortID(session.ID), session.Backend, conversation.ID, conversation.Source)
	return nil
}
