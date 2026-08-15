package brief

import "github.com/zamborg/heikou/internal/transcript"

// repliedPrefix marks a line that is what the session said when it finished,
// rather than what it is doing. Without it a finished reply is indistinguishable
// in a row from a message the user sent, which is the same confusion the
// approximate mark exists to prevent one level up.
const repliedPrefix = "replied · "

// activityLine phrases one reading of a runner's record for a row.
//
// The verbs are the deliberate part. `internal/transcript` reports what the
// record contains — a tool name and the argument it acted on — and stops there,
// because "Edit /long/path/brief.go" is the fact and "editing brief.go" is a
// choice about a cell twenty columns wide. Keeping the choice here means the
// details pane and a future JSON projection can make a different one from the
// same reading.
//
// Every phrase is a statement about a record, never about the present. A tool
// call with no result after it is reported as running, not as blocked, because
// the transcript cannot tell a command that is executing from one waiting for
// the user to approve it. Saying "waiting on you" would need a signal that
// reports the runner's current state; see todos/runner-activity.md.
func activityLine(item transcript.Activity) string {
	if !item.Known() {
		return ""
	}
	switch item.Kind {
	case transcript.ActivityRunning:
		return runningLine(item)
	case transcript.ActivityReplied:
		if item.Detail == "" {
			return "replied"
		}
		return repliedPrefix + item.Detail
	default:
		return "working"
	}
}

func runningLine(item transcript.Activity) string {
	subject := item.Subject()
	if subject == "" {
		// A call whose argument this package does not recognise still says which
		// tool is running, which is most of the answer.
		return "running " + item.Tool
	}
	if verb := activityVerbs[item.Tool]; verb != "" {
		return verb + " " + subject
	}
	return item.Tool + " " + subject
}

// activityVerbs reads a tool name as something a person would say a session is
// doing. A tool that is not here is named rather than described, which is why
// an unfamiliar or user-supplied tool degrades to "MyTool src/main.go" instead
// of to a wrong verb.
var activityVerbs = map[string]string{
	"Bash":         "running",
	"BashOutput":   "running",
	"KillShell":    "stopping",
	"Edit":         "editing",
	"MultiEdit":    "editing",
	"Write":        "writing",
	"NotebookEdit": "editing",
	"Read":         "reading",
	"NotebookRead": "reading",
	"Grep":         "searching for",
	"Glob":         "looking for",
	"WebFetch":     "fetching",
	"WebSearch":    "searching the web for",
	"Task":         "delegating",
	"Agent":        "delegating",
	"Skill":        "running",
}
