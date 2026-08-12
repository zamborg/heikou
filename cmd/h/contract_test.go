package main

// The --json surface is a published contract, not an implementation detail.
// skills/manage-heikou/SKILL.md tells the pilot which keys to read, and the
// pilot has no way to notice that a key was renamed — it just stops finding the
// data and starts guessing. These tests make the documentation and the structs
// fail together.

import (
	"reflect"
	"strings"
	"testing"

	manageheikou "github.com/zamborg/heikou/skills/manage-heikou"
)

// documentedStates is the set skills/manage-heikou promises. A state the pilot
// has never been told about is one it will describe badly, and rule 3 of
// AGENTS.md exists precisely to stop it inventing a description.
var documentedStates = map[string]bool{
	"live":         true,
	"exited":       true,
	"stopped":      true,
	"start_failed": true,
	"unavailable":  true,
}

// jsonTags reports the JSON key of every field on a struct type, ignoring the
// omitempty and string options.
func jsonTags(value any) map[string]bool {
	tags := map[string]bool{}
	structType := reflect.TypeOf(value)
	for index := 0; index < structType.NumField(); index++ {
		tag := structType.Field(index).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		tags[strings.Split(tag, ",")[0]] = true
	}
	return tags
}

// documentedFields pulls the backticked names out of the SKILL.md paragraph
// that enumerates the h list --json keys. Reading the shipped text rather than
// a copy of it means the test fails when the documentation drifts, which is the
// failure that actually hurts the pilot.
func documentedFields(t *testing.T, marker string) []string {
	t.Helper()
	start := strings.Index(manageheikou.Skill, marker)
	if start < 0 {
		t.Fatalf("SKILL.md no longer contains %q; the contract test cannot find the field list", marker)
	}
	paragraph := manageheikou.Skill[start:]
	if end := strings.Index(paragraph, "\n\n"); end >= 0 {
		paragraph = paragraph[:end]
	}
	var fields []string
	for index, part := range strings.Split(paragraph, "`") {
		// Odd indices are the backticked spans.
		if index%2 == 1 {
			fields = append(fields, part)
		}
	}
	if len(fields) == 0 {
		t.Fatalf("found no backticked field names after %q", marker)
	}
	return fields
}

func TestSkillDocumentsOnlyFieldsTheJSONSurfaceEmits(t *testing.T) {
	// SKILL.md documents both lists in a single paragraph, so one extraction
	// covers the workstream keys and the session keys together.
	promised := documentedFields(t, "returns per workstream:")

	workstreamTags := jsonTags(cliWorkstreamJSON{})
	sessionTags := jsonTags(cliSessionJSON{})
	known := map[string]bool{}
	for tag := range workstreamTags {
		known[tag] = true
	}
	for tag := range sessionTags {
		known[tag] = true
	}

	for _, field := range promised {
		if !known[field] {
			t.Errorf("SKILL.md promises the pilot a %q key that h list --json does not emit", field)
		}
	}

	// The reverse direction is a warning, not a failure: an undocumented key is
	// merely unused, while a documented-but-missing key sends the pilot looking
	// for data that is not there.
	documented := map[string]bool{}
	for _, field := range promised {
		documented[field] = true
	}
	for tag := range known {
		if !documented[tag] {
			t.Logf("h list --json emits %q, which SKILL.md does not document", tag)
		}
	}
}

func TestSkillDocumentsTheLoadBearingSessionKeys(t *testing.T) {
	// These are the keys AGENTS.md instructs the pilot to act on: how to name a
	// session, where to write notes, and how to report honest state. Losing any
	// one of them silently degrades the pilot rather than breaking it.
	sessionTags := jsonTags(cliSessionJSON{})
	for _, field := range []string{
		"id", "runner", "state", "title", "display_title",
		"initial_prompt", "workstream", "root", "exit_code",
	} {
		if !sessionTags[field] {
			t.Errorf("cliSessionJSON no longer emits %q, which the pilot instructions rely on", field)
		}
	}
	workstreamTags := jsonTags(cliWorkstreamJSON{})
	for _, field := range []string{"id", "name", "artifact_dir", "roots"} {
		if !workstreamTags[field] {
			t.Errorf("cliWorkstreamJSON no longer emits %q, which the pilot instructions rely on", field)
		}
	}
}

func TestSkillDocumentsEveryStateTheCLICanReport(t *testing.T) {
	for state := range documentedStates {
		if !strings.Contains(manageheikou.Skill, "`"+state+"`") {
			t.Errorf("SKILL.md does not document the %q state", state)
		}
	}
	// AGENTS.md rule 3 lists the same set, because that is where the pilot is
	// told not to invent a status of its own.
	for state := range documentedStates {
		if !strings.Contains(manageheikou.Agents, "`"+state+"`") {
			t.Errorf("AGENTS.md does not list the %q state in its honest-reporting rule", state)
		}
	}
}
