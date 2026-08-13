package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zamborg/heikou/internal/brief"
	"github.com/zamborg/heikou/internal/config"
	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/heikou"
)

// briefTestModel is a dashboard at a fixed width with the default brief
// configuration, which is what the row assertions below measure.
func briefTestModel(width int) Model {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width = width
	return model
}

func briefTestSession(title, prompt, latest string) control.Session {
	session := testDurableSession("018f0000-0000-4000-8000-0000000000b1", "", heikou.BackendClaude, prompt, "/tmp/project", time.Now())
	session.Record.Title = title
	session.LastUserMessage = latest
	return session
}

func TestBriefGivesLeadAndDetailSeparateBudgets(t *testing.T) {
	item := briefTestModel(80).sessionBrief(briefTestSession(
		"Fix flaky OAuth tests", "investigate", "also check whether the retry hides the root cause"))

	// Wide enough for both: the detail must carry real message text rather than
	// the truncated stub a single shared budget used to produce.
	rendered := renderBrief(item, 60)
	if lipgloss.Width(rendered) > 60 {
		t.Fatalf("brief overflowed its width: %d > 60 (%q)", lipgloss.Width(rendered), rendered)
	}
	lead, detail, found := strings.Cut(rendered, briefSeparator)
	if !found {
		t.Fatalf("brief did not render a detail at width 60: %q", rendered)
	}
	if !strings.HasPrefix(lead, "Fix flaky OAuth tests") {
		t.Fatalf("lead was truncated while the detail had room: %q", lead)
	}
	if lipgloss.Width(detail) < briefMinDetailWidth {
		t.Fatalf("detail rendered below its minimum: %q", detail)
	}
	if !strings.HasPrefix(detail, "also check") {
		t.Fatalf("detail did not start with the message: %q", detail)
	}
}

func TestBriefDropsTheDetailRatherThanShowingAStub(t *testing.T) {
	item := briefTestModel(80).sessionBrief(briefTestSession("Fix flaky OAuth tests", "investigate", "also check the retry"))
	for _, width := range []int{1, 6, 12, 20, 30} {
		rendered := renderBrief(item, width)
		if lipgloss.Width(rendered) > width {
			t.Fatalf("brief overflowed at width %d: %q", width, rendered)
		}
		if !strings.Contains(rendered, briefSeparator) {
			continue
		}
		_, detail, _ := strings.Cut(rendered, briefSeparator)
		if lipgloss.Width(detail) < briefMinDetailWidth {
			t.Fatalf("width %d rendered a detail stub: %q", width, rendered)
		}
	}
}

// The provenance mark is the reason the seam exists: text Heikou was told must
// not land unmarked in the columns where text the user typed appears.
func TestRenderBriefMarksTextThatWasNotProven(t *testing.T) {
	item := brief.Brief{
		Lead:   brief.Fragment{Text: "probably an OAuth fix", Source: brief.SourceTitle},
		Detail: brief.Fragment{Text: "also check the retry", Source: brief.SourceLatest, Proven: true},
	}
	rendered := renderBrief(item, 80)
	if !strings.HasPrefix(rendered, briefApproximateMark+"probably an OAuth fix") {
		t.Fatalf("unproven lead was not marked approximate: %q", rendered)
	}
	if strings.Contains(rendered, briefApproximateMark+"also check") {
		t.Fatalf("observed detail was marked approximate: %q", rendered)
	}

	proven := brief.Brief{Lead: brief.Fragment{Text: "Fix OAuth", Source: brief.SourceTitle, Proven: true}}
	if rendered := renderBrief(proven, 80); strings.Contains(rendered, briefApproximateMark) {
		t.Fatalf("proven lead was marked approximate: %q", rendered)
	}
}

func TestBriefDetailLabelFollowsTheSourceThatFilledIt(t *testing.T) {
	model := briefTestModel(80)
	if got := model.sessionSecondaryDetail(briefTestSession("Fix OAuth", "investigate", "also the retry")); got != "latest via Heikou · also the retry" {
		t.Fatalf("labelled detail = %q", got)
	}
	if got := model.sessionSecondaryDetail(briefTestSession("Fix OAuth", "investigate", "")); got != "initial task · investigate" {
		t.Fatalf("labelled detail = %q", got)
	}
	if got := model.sessionSecondaryDetail(briefTestSession("", "investigate", "")); got != "" {
		t.Fatalf("labelled detail = %q, want empty", got)
	}
}

// The row is assembled from fixed-width columns plus the brief. When those
// column widths are miscounted the row overflows and the final truncate silently
// clips its tail — which is how the runtime column lost the trailing minute of
// "23h59m" without any test noticing.
func TestSessionRowsFillExactlyTheirWidth(t *testing.T) {
	now := time.Now()
	session := briefTestSession("Fix flaky OAuth tests", "investigate the flaky test", "also check whether the retry hides the root cause")
	session.Runtime.StartedAt = now.Add(-23*time.Hour - 59*time.Minute)
	session.CreatedAt = session.Runtime.StartedAt

	for _, width := range []int{40, 48, 63, 64, 80, 95, 96, 100, 120, 160} {
		model := briefTestModel(width)
		if row := model.renderSessionRow(session, false); ansi.StringWidth(row) != width {
			t.Fatalf("row at width %d rendered %d columns: %q", width, ansi.StringWidth(row), ansi.Strip(row))
		}
		if width >= sessionRowRichMinWidth {
			if plain := ansi.Strip(model.renderSessionRow(session, false)); !strings.Contains(plain, "23h59m") {
				t.Fatalf("dashboard row at width %d clipped its runtime: %q", width, plain)
			}
		}
	}
}

// Widening the terminal by one column can cost the summary the width of a
// column that just appeared, and no more. It cannot be made free — the root
// basename has to take its columns from somewhere — but it must stay bounded by
// what it buys. It did not: a sixteen-column root plus a two-column miscount
// took seventeen columns away from the summary at exactly the terminal width
// where the summary was already too tight to show a message.
func TestWideningTheTerminalCostsTheSummaryNoMoreThanTheColumnItBuys(t *testing.T) {
	session := briefTestSession("Fix flaky OAuth tests", "investigate", "also check whether the retry hides the root cause")
	budget := rowRootWidth + 2
	previous := 0
	for width := sessionRowRichMinWidth; width <= 200; width++ {
		summary := lipgloss.Width(briefTestModel(width).sessionRowSummary(session, briefWidthForRow(width)))
		if drop := previous - summary; drop > budget {
			t.Fatalf("growing the row to %d columns cost the summary %d columns, more than the %d it bought",
				width, drop, budget)
		}
		previous = summary
	}
}

// briefWidthForRow mirrors the dashboard row's budget so the test measures the
// same number the renderer hands the brief.
func briefWidthForRow(width int) int {
	fixed := 44
	if width >= 96 {
		fixed += rowRootWidth + 2
	}
	return max(1, width-fixed)
}

// The whole point of the seam: text a configured source reported has to reach
// the row, carrying the mark that says Heikou was told it rather than saw it.
func TestConfiguredSourceReachesTheRowMarkedApproximate(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 120, 30
	model.settings.Brief = config.BriefConfig{
		Lead:    []string{"status", "title", "prompt"},
		Detail:  []string{"latest"},
		Sources: map[string]config.BriefSourceConfig{"status": {Command: []string{"agent-status"}, IntervalSeconds: 5, TimeoutSeconds: 2}},
	}

	session := briefTestSession("Fix flaky OAuth tests", "investigate", "also check the retry")
	model.snapshot.Sessions = []control.Session{session}
	model.setSnapshot(model.snapshot)

	// Before the first pass the source has nothing, so the slot falls through.
	if plain := ansi.Strip(model.renderSessionRow(session, false)); !strings.Contains(plain, "Fix flaky OAuth tests") {
		t.Fatalf("unobserved source did not fall through to the title: %q", plain)
	}

	model.briefObservations = brief.Observations{
		{Session: session.ID, Source: "status"}: {Text: "esc to interrupt"},
	}
	plain := ansi.Strip(model.renderSessionRow(session, false))
	if !strings.Contains(plain, briefApproximateMark+"esc to interrupt") {
		t.Fatalf("configured source did not reach the row marked approximate: %q", plain)
	}
	if !strings.Contains(plain, "also check the retry") {
		t.Fatalf("detail slot was lost when the lead changed source: %q", plain)
	}
}

// Reloading settings must not leave a removed source's text on screen.
func TestReloadingSettingsDropsARemovedSourcesText(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 120, 30
	model.settings.Brief = config.BriefConfig{
		Lead:    []string{"status", "title"},
		Sources: map[string]config.BriefSourceConfig{"status": {Command: []string{"agent-status"}, IntervalSeconds: 5, TimeoutSeconds: 2}},
	}
	session := briefTestSession("Fix flaky OAuth tests", "investigate", "")
	model.snapshot.Sessions = []control.Session{session}
	model.setSnapshot(model.snapshot)
	model.briefObservations = brief.Observations{{Session: session.ID, Source: "status"}: {Text: "esc to interrupt"}}

	updated, _ := model.Update(settingsMsg{settings: config.Default()})
	reloaded, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T", updated)
	}
	plain := ansi.Strip(reloaded.renderSessionRow(session, false))
	if strings.Contains(plain, "esc to interrupt") {
		t.Fatalf("a removed source's text survived a settings reload: %q", plain)
	}
	if !strings.Contains(plain, "Fix flaky OAuth tests") {
		t.Fatalf("reloaded row lost its default lead: %q", plain)
	}
}

// The notice and error lines are feedback for what the user just did, and they
// are only cleared by the next keystroke. A background observation pass writing
// there would wipe the result of the user's last action every interval.
func TestBriefPassesDoNotOverwriteWhatTheUserJustDid(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 120, 30
	model.notice = "renamed workstream"
	model.errorText = "delete refused while a pane remains"

	updated, _ := model.Update(briefMsg{
		generation:   model.briefFetch.generation,
		observations: brief.Observations{},
		report:       brief.Report{Deferred: 4, Failures: []error{errors.New("agent-status: executable file not found")}},
	})
	after, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T", updated)
	}
	if after.notice != "renamed workstream" || after.errorText != "delete refused while a pane remains" {
		t.Fatalf("a background pass overwrote action feedback: notice=%q error=%q", after.notice, after.errorText)
	}
}

// It still has to be visible somewhere, or a capped pass reads as full coverage
// and a broken command reads as a source with nothing to say.
func TestBriefSourceHealthIsVisibleInSettings(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 120, 40
	model.settings.Brief = config.BriefConfig{
		Lead:    []string{"status", "title"},
		Sources: map[string]config.BriefSourceConfig{"status": {Command: []string{"agent-status"}, IntervalSeconds: 5, TimeoutSeconds: 2}},
	}

	if healthy := ansi.Strip(strings.Join(model.briefHealthLines(), "\n")); healthy != "" {
		t.Fatalf("a healthy configuration reported status: %q", healthy)
	}

	model.briefReport = brief.Report{Deferred: 4, Failures: []error{errors.New("agent-status: executable file not found")}}
	settings := ansi.Strip(strings.Join(model.settingsLines(), "\n"))
	if !strings.Contains(settings, "4 session updates deferred") {
		t.Fatalf("a capped pass was invisible: %q", settings)
	}
	if !strings.Contains(settings, "executable file not found") {
		t.Fatalf("a failing source was invisible: %q", settings)
	}
	if !strings.Contains(settings, "agent-status") {
		t.Fatalf("settings did not list the source the health line is about: %q", settings)
	}
}
