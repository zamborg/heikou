// Package format holds the presentation helpers that every Heikou surface
// shares: how long a session has been running, how an id is abbreviated, how a
// path is shortened, and how untrusted text is made safe to print on one line.
//
// These lived separately in cmd/h and internal/ui and drifted. The terminal
// dashboard learned to render a three-day-old session as "3d" while h list kept
// reporting "72h00m" for the same session, because the two copies of
// formatDuration were edited at different times. A user comparing the two
// surfaces saw a discrepancy that no test could catch, since each copy was
// self-consistent.
//
// The package deliberately imports nothing from the rest of Heikou. It is a
// leaf, so any surface can use it without creating a dependency cycle, and
// there is never a reason to write a second copy.
package format

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// Duration renders an elapsed time in the most significant unit that still
// carries information, so a column stays narrow without becoming a lie.
func Duration(value time.Duration) string {
	if value < time.Minute {
		return fmt.Sprintf("%ds", max(0, int(value.Seconds())))
	}
	if value < time.Hour {
		return fmt.Sprintf("%dm", int(value.Minutes()))
	}
	if value < 24*time.Hour {
		return fmt.Sprintf("%dh%02dm", int(value.Hours()), int(value.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(value.Hours()/24))
}

// RelativeTime renders how long ago something happened. A moment that has only
// just passed reads as "now" rather than a jittering second count.
func RelativeTime(value, now time.Time) string {
	delta := now.Sub(value)
	if delta < 0 {
		delta = 0
	}
	if delta < 2*time.Second {
		return "now"
	}
	return Duration(delta) + " ago"
}

// ShortID abbreviates a durable identifier to the prefix a human types.
//
// Separators are removed first so that the abbreviation counts six significant
// characters rather than six bytes of a UUID's layout. Heikou's ids are UUIDs,
// whose first group is already eight hex digits, so this matters only if the id
// format ever changes — which is exactly when a silent disagreement between two
// copies of this function would have been most expensive.
func ShortID(id string) string {
	clean := strings.ReplaceAll(id, "-", "")
	if len(clean) <= 6 {
		return clean
	}
	return clean[:6]
}

// CompactPath replaces the user's home directory with ~ so a root fits a column
// without losing the part that identifies it.
func CompactPath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && (path == home || strings.HasPrefix(path, home+string(filepath.Separator))) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// Sanitize strips ANSI sequences and control characters from text that came
// from an agent's terminal, keeping newlines and tabs so multi-line output
// remains readable. Everything Heikou prints from a pane passes through here:
// an escape sequence that survived would let a session's output move the
// cursor, repaint the dashboard, or hide what it did.
func Sanitize(value string) string {
	value = ansi.Strip(value)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, value)
}

// OneLine collapses sanitized text onto a single line, which is what a table
// cell or a status row can actually hold. It is idempotent, so wrapping text
// that was already flattened is harmless.
func OneLine(value string) string {
	return strings.Join(strings.Fields(Sanitize(value)), " ")
}
