package format

import (
	"strings"
	"testing"
	"time"
)

func TestDurationPicksTheUnitThatStillCarriesInformation(t *testing.T) {
	for _, testCase := range []struct {
		value time.Duration
		want  string
	}{
		{-time.Second, "0s"},
		{0, "0s"},
		{45 * time.Second, "45s"},
		{time.Minute, "1m"},
		{90 * time.Minute, "1h30m"},
		{time.Hour, "1h00m"},
		{23*time.Hour + 59*time.Minute, "23h59m"},
		// The boundary that drifted: cmd/h used to stop at hours and report a
		// three-day-old session as 72h00m while the dashboard said 3d.
		{24 * time.Hour, "1d"},
		{72 * time.Hour, "3d"},
	} {
		if got := Duration(testCase.value); got != testCase.want {
			t.Errorf("Duration(%s) = %q, want %q", testCase.value, got, testCase.want)
		}
	}
}

func TestRelativeTimeReadsAsNowInsideTheJitterWindow(t *testing.T) {
	now := time.Now()
	if got := RelativeTime(now.Add(-time.Second), now); got != "now" {
		t.Errorf("RelativeTime one second ago = %q, want %q", got, "now")
	}
	// A clock that is behind must not produce a negative age.
	if got := RelativeTime(now.Add(time.Hour), now); got != "now" {
		t.Errorf("RelativeTime in the future = %q, want %q", got, "now")
	}
	if got := RelativeTime(now.Add(-90*time.Minute), now); got != "1h30m ago" {
		t.Errorf("RelativeTime 90 minutes ago = %q, want %q", got, "1h30m ago")
	}
}

func TestShortIDCountsSignificantCharactersNotBytes(t *testing.T) {
	for _, testCase := range []struct {
		id   string
		want string
	}{
		{"018f0000-0000-4000-8000-000000000001", "018f00"},
		{"abc", "abc"},
		{"", ""},
		// Separators are removed before counting, so an id whose groups are
		// shorter than six characters still yields six significant ones.
		{"ab-cd-ef-gh", "abcdef"},
	} {
		if got := ShortID(testCase.id); got != testCase.want {
			t.Errorf("ShortID(%q) = %q, want %q", testCase.id, got, testCase.want)
		}
	}
}

func TestOneLineRemovesEscapesAndCollapsesWhitespace(t *testing.T) {
	// A pane can emit anything. What reaches a table cell must not be able to
	// move the cursor or span rows.
	// A bell is removed outright rather than turned into a space: it never
	// stood for a word boundary, so inventing one would change the text.
	hostile := "\x1b[31mred\x1b[0m\ttabbed\nnewline\r\ncarriage\x07bell"
	got := OneLine(hostile)
	want := "red tabbed newline carriagebell"
	if got != want {
		t.Fatalf("OneLine(hostile) = %q, want %q", got, want)
	}
	if strings.ContainsAny(got, "\x1b\x07\r\n\t") {
		t.Errorf("OneLine left a control character in %q", got)
	}
}

func TestOneLineIsIdempotent(t *testing.T) {
	// Several call sites wrap text that a caller already flattened. Flattening
	// twice has to be free, or those sites become subtly order-dependent.
	value := "  \x1b[1mspaced\x1b[0m\n\nout  "
	once := OneLine(value)
	if twice := OneLine(once); twice != once {
		t.Errorf("OneLine is not idempotent: %q then %q", once, twice)
	}
}

func TestSanitizeKeepsTheLineStructureOneLineDiscards(t *testing.T) {
	// Sanitize is what the preview pane uses, and it must preserve the newlines
	// and tabs that make multi-line agent output readable.
	got := Sanitize("first\n\tindented\x00nul")
	if got != "first\n\tindented"+"nul" {
		t.Errorf("Sanitize dropped or kept the wrong characters: %q", got)
	}
}

func TestCompactPathOnlyRewritesTheHomePrefix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := CompactPath(home + "/projects/heikou"); got != "~/projects/heikou" {
		t.Errorf("CompactPath inside home = %q, want %q", got, "~/projects/heikou")
	}
	if got := CompactPath(home); got != "~" {
		t.Errorf("CompactPath of home itself = %q, want %q", got, "~")
	}
	// A sibling directory whose name merely starts with the home path must not
	// be rewritten; only a real path boundary counts.
	if got := CompactPath(home + "-backup/notes"); got != home+"-backup/notes" {
		t.Errorf("CompactPath rewrote a sibling directory: %q", got)
	}
	if got := CompactPath("/etc/hosts"); got != "/etc/hosts" {
		t.Errorf("CompactPath outside home = %q, want %q", got, "/etc/hosts")
	}
}
