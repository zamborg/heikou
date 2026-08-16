package ui

// boundChords is every key Heikou itself answers to: the dashboard, the help
// and settings screens, resize mode, and the composer's editing chords.
//
// It exists because binding a chord in a key switch and reserving it in
// internal/config were two separate edits with nothing between them, and the
// reserved list fell behind. Ctrl-N, Ctrl-O and Ctrl-T were bound here and
// rebindable there, so a settings file could aim "reply" at Ctrl-N, load
// without complaint, and cost the user "create a workstream" permanently.
//
// Two tests hold the chain together so neither end can move alone:
//
//   - TestEveryChordTheUIBindsIsDeclaredHere reads this package's own source and
//     compares the key literals its handlers dispatch on against this list, in
//     both directions. The list therefore cannot be a stale copy of the
//     switches, and the switches stay readable with their literals in place.
//   - TestEveryBoundChordIsReservedFromComposerBindings compares this list
//     against config.ReservedComposerKeys, in both directions.
//
// So adding a case to a key switch fails the tests until the chord is declared
// here, and declaring it here fails them until internal/config reserves it and
// says what the key does.
//
// internal/architecture decides which end holds the assertions: ui may import
// config and config may not import ui, so the comparison lives here.
var boundChords = []string{
	// Lifecycle, screens, and panels.
	"ctrl+c", "ctrl+g", "ctrl+s", "ctrl+x", "esc", "f1", "f2", "f3", "?",

	// Organizing, which reads the selected row for its noun.
	"ctrl+n", "ctrl+o", "ctrl+r", "ctrl+t", archiveChord, "shift+down", "shift+up",

	// Moving the selection, and paging the help and settings viewports.
	"alt+down", "alt+up", "down", "end", "home", "left", "meta+down", "meta+up",
	"pgdown", "pgup", "right", "up",

	// Committing, and the settings screen's own two letters.
	"enter", "e", "r",

	// Composer editing. Several chords are aliases for one action because the
	// terminal decides which modifier combinations reach Heikou at all.
	"alt+b", "alt+backspace", "alt+delete", "alt+enter", "alt+f", "alt+left",
	"alt+right", "backspace", "ctrl+a", "ctrl+b", "ctrl+d", "ctrl+e", "ctrl+end",
	"ctrl+f", "ctrl+h", "ctrl+home", "ctrl+j", "ctrl+k", "ctrl+left", "ctrl+p",
	"ctrl+right", "ctrl+u", "ctrl+w", "delete", "meta+b", "meta+backspace",
	"meta+delete", "meta+enter", "meta+f", "meta+left", "meta+right",
	"shift+enter", "super+backspace", "super+delete", "super+down", "super+left",
	"super+right", "super+up",
}
