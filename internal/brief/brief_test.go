package brief

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zamborg/heikou/internal/config"
	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/workstream"
)

func testSession(title, prompt, latest string) control.Session {
	return control.Session{
		ID:              "018f0000-0000-4000-8000-0000000000b1",
		Backend:         heikou.BackendClaude,
		Prompt:          prompt,
		LastUserMessage: latest,
		Root:            "/tmp/project",
		Status:          control.StatusLive,
		Durable:         true,
		Record:          workstream.SessionRecord{Title: title},
	}
}

func defaultResolve(session control.Session) Brief {
	settings := config.Default().Brief
	return LayoutFrom(settings).Resolve(session, NewRegistry(settings, nil))
}

// Configuration validates slot names against config.BuiltinBriefSources while
// rendering resolves them against the identifiers here. If those two lists
// disagree, a settings file is accepted that names a source nothing fills, and
// the row silently falls through to the next one.
func TestBuiltinSourceNamesMatchTheNamesConfigurationAccepts(t *testing.T) {
	settings := config.Default().Brief
	registry := NewRegistry(settings, nil)
	for _, name := range config.BuiltinBriefSources {
		if _, ok := registry[SourceID(name)]; !ok {
			t.Fatalf("configuration accepts source %q that the registry cannot fill", name)
		}
	}
	for id := range registry {
		if !slices.Contains(config.BuiltinBriefSources, string(id)) {
			t.Fatalf("registry fills source %q that configuration will reject", id)
		}
	}
}

func TestLayoutFillsSlotsInOrder(t *testing.T) {
	tests := []struct {
		name       string
		title      string
		prompt     string
		latest     string
		wantLead   SourceID
		wantDetail SourceID
	}{
		{name: "title leads and latest follows", title: "Fix OAuth", prompt: "investigate", latest: "also the retry",
			wantLead: SourceTitle, wantDetail: SourceLatest},
		{name: "prompt leads when untitled", prompt: "investigate", latest: "also the retry",
			wantLead: SourcePrompt, wantDetail: SourceLatest},
		{name: "titled session falls back to its prompt", title: "Fix OAuth", prompt: "investigate",
			wantLead: SourceTitle, wantDetail: SourcePrompt},
		{name: "runner labels a session with no text at all",
			wantLead: SourceRunner, wantDetail: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := defaultResolve(testSession(test.title, test.prompt, test.latest))
			if item.Lead.Source != test.wantLead {
				t.Fatalf("lead source = %q, want %q", item.Lead.Source, test.wantLead)
			}
			if item.Detail.Source != test.wantDetail {
				t.Fatalf("detail source = %q, want %q", item.Detail.Source, test.wantDetail)
			}
		})
	}
}

// An untitled session leads with its prompt, so the prompt must not also fill
// the detail. This one rule replaced a hand-written special case, and it is the
// one that breaks first if the layout gains a source.
func TestLayoutNeverRepeatsTheSourceItLeadsWith(t *testing.T) {
	item := defaultResolve(testSession("", "investigate the flaky test", ""))
	if item.Lead.Source != SourcePrompt {
		t.Fatalf("lead source = %q, want %q", item.Lead.Source, SourcePrompt)
	}
	if !item.Detail.Empty() {
		t.Fatalf("detail repeated the lead source: %+v", item.Detail)
	}
}

func TestBuiltinSourcesAreProvenAndObservedSourcesAreNot(t *testing.T) {
	item := defaultResolve(testSession("Fix OAuth", "investigate", "also the retry"))
	if !item.Lead.Proven || !item.Detail.Proven {
		t.Fatalf("built-in sources reported themselves unproven: %+v", item)
	}

	settings := config.BriefConfig{
		Lead:    []string{"status", "title"},
		Detail:  []string{"latest"},
		Sources: map[string]config.BriefSourceConfig{"status": {Command: []string{"true"}, IntervalSeconds: 10, TimeoutSeconds: 3}},
	}
	session := testSession("Fix OAuth", "investigate", "also the retry")
	observations := Observations{{Session: session.ID, Source: "status"}: {Text: "waiting for input"}}
	observed := LayoutFrom(settings).Resolve(session, NewRegistry(settings, observations))
	if observed.Lead.Source != "status" {
		t.Fatalf("configured source did not fill the lead: %+v", observed.Lead)
	}
	if observed.Lead.Proven {
		t.Fatal("a command's output reported itself as proven; Heikou cannot verify what it was told")
	}
}

// A configured source with nothing cached must fall through rather than render
// an empty lead, or a dashboard shows blank rows until the first pass lands.
func TestObservedSourceFallsThroughUntilItHasBeenObserved(t *testing.T) {
	settings := config.BriefConfig{
		Lead:    []string{"status", "title"},
		Sources: map[string]config.BriefSourceConfig{"status": {Command: []string{"true"}, IntervalSeconds: 10, TimeoutSeconds: 3}},
	}
	session := testSession("Fix OAuth", "investigate", "")
	item := LayoutFrom(settings).Resolve(session, NewRegistry(settings, nil))
	if item.Lead.Source != SourceTitle {
		t.Fatalf("unobserved source did not fall through: %+v", item.Lead)
	}
}

// Sanitization itself belongs to internal/format, which every surface shares.
// What is local to a brief is the bound: a row can only show so much, and a
// command that prints a paragraph must not put one in memory for every session.
func TestBriefTextBoundsWhatARowCanHold(t *testing.T) {
	if got := briefText(strings.Repeat("a", maxTextRunes+50)); len([]rune(got)) != maxTextRunes {
		t.Fatalf("text was not bounded: %d runes", len([]rune(got)))
	}
	if got := briefText("\x1b[31mred\x1b[0m  text"); got != "red text" {
		t.Fatalf("brief text did not go through the shared sanitizer: %q", got)
	}
	if got := briefText("héllo · 世界"); got != "héllo · 世界" {
		t.Fatalf("multibyte text was mangled: %q", got)
	}
}

func TestFragmentLabelsNameTheSource(t *testing.T) {
	for id, want := range map[SourceID]string{
		SourceLatest: "latest via Heikou",
		SourcePrompt: "initial task",
		SourceTitle:  "title",
		SourceRunner: "runner",
		"status":     "status",
	} {
		if got := (Fragment{Source: id}).Label(); got != want {
			t.Fatalf("label for %q = %q, want %q", id, got, want)
		}
	}
}

func TestLayoutFromConfigCarriesAnEmptyDetail(t *testing.T) {
	layout := LayoutFrom(config.BriefConfig{Lead: []string{"title"}, Detail: nil})
	item := layout.Resolve(testSession("Fix OAuth", "investigate", "also the retry"), NewRegistry(config.BriefConfig{}, nil))
	if item.Lead.Text != "Fix OAuth" {
		t.Fatalf("lead = %q", item.Lead.Text)
	}
	if !item.Detail.Empty() {
		t.Fatalf("empty detail slot still rendered: %+v", item.Detail)
	}
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse time: %v", err)
	}
	return parsed
}
