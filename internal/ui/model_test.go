package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/zamborg/heikou/internal/config"
	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/workstream"
)

type fakeController struct {
	snapshot               control.Snapshot
	startRequest           control.StartRequest
	adoptSession           string
	adoptTarget            string
	stopped                string
	deleted                string
	movedSession           string
	movedTarget            string
	sentSession            string
	sentText               string
	captureText            string
	replacedRootWorkstream string
	replacedRootCurrent    string
	replacedRootValue      string
	removedRootWorkstream  string
	removedRootValue       string
	reorderedWorkstream    string
	reorderedDelta         int
	reorderNoop            bool
	titledSession          string
	titleValue             string
}

func (f *fakeController) Snapshot(context.Context) (control.Snapshot, error) { return f.snapshot, nil }
func (f *fakeController) Find(context.Context, string) (control.Session, error) {
	return control.Session{}, nil
}
func (f *fakeController) Start(_ context.Context, request control.StartRequest) (control.Session, error) {
	f.startRequest = request
	return control.Session{ID: "018f0000-0000-4000-8000-000000000099", Backend: request.Backend, Prompt: request.Prompt, Root: request.Root, WorkstreamID: request.WorkstreamID, Durable: true, Status: control.StatusLive}, nil
}
func (f *fakeController) Send(_ context.Context, id, message string) error {
	f.sentSession, f.sentText = id, message
	return nil
}
func (f *fakeController) Capture(context.Context, string, int) (string, error) {
	return f.captureText, nil
}
func (f *fakeController) Stop(_ context.Context, id string) error {
	f.stopped = id
	return nil
}
func (f *fakeController) DeleteSession(_ context.Context, id string) error {
	f.deleted = id
	return nil
}
func (f *fakeController) AttachCommand(context.Context, string) (*exec.Cmd, error) {
	return exec.Command("true"), nil
}
func (f *fakeController) CreateWorkstream(context.Context, string, string, []string) (workstream.Workstream, error) {
	return workstream.Workstream{}, nil
}
func (f *fakeController) RenameWorkstream(context.Context, string, string) error { return nil }
func (f *fakeController) SetSessionTitle(_ context.Context, id, title string) error {
	f.titledSession, f.titleValue = id, title
	return nil
}
func (f *fakeController) ReorderWorkstream(_ context.Context, id string, delta int) (bool, error) {
	f.reorderedWorkstream, f.reorderedDelta = id, delta
	return !f.reorderNoop, nil
}
func (f *fakeController) ArchiveWorkstream(context.Context, string) error { return nil }
func (f *fakeController) MoveSession(_ context.Context, sessionID, workstreamID string) error {
	f.movedSession, f.movedTarget = sessionID, workstreamID
	return nil
}

func (f *fakeController) AdoptSession(_ context.Context, sessionID, workstreamID string) (control.Session, error) {
	f.adoptSession, f.adoptTarget = sessionID, workstreamID
	return control.Session{}, nil
}
func (f *fakeController) AddRoot(context.Context, string, string) error { return nil }
func (f *fakeController) ReplaceRoot(_ context.Context, workstreamID, current, replacement string) error {
	f.replacedRootWorkstream, f.replacedRootCurrent, f.replacedRootValue = workstreamID, current, replacement
	return nil
}
func (f *fakeController) RemoveRoot(_ context.Context, workstreamID, root string) error {
	f.removedRootWorkstream, f.removedRootValue = workstreamID, root
	return nil
}

func TestViewStaysWithinTerminalAtCommonSizes(t *testing.T) {
	now := time.Now()
	container := testWorkstream("018f0000-0000-4000-8000-000000000001", "Unicode project", []string{"/tmp/a directory with spaces/日本語-project"}, now)
	session := testDurableSession("018f0000-0000-4000-8000-000000000002", container.ID, heikou.BackendClaude, "Implement 日本語 support with 👩🏽‍💻 emoji and e\u0301 combining characters in a deliberately very long task title", container.Roots[0], now)
	for _, size := range []struct{ width, height int }{{40, 15}, {80, 24}, {120, 40}} {
		model, _ := newTestModel("/tmp/a directory with spaces", heikou.BackendCodex)
		model.width, model.height = size.width, size.height
		model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{container}, Sessions: []control.Session{session}, StatePath: "/tmp/heikou-state.json"})
		model.selected = sessionRowKey(session)
		model.restoreSelection()
		model.previewID = session.ID
		model.preview = "output 日本語\nwide 👩🏽‍💻 and combining e\u0301\n" + strings.Repeat("long output ", 30)
		assertViewFits(t, model.View().Content, size.width, size.height)
	}
}

func TestDashboardClipsAtTinyTerminalHeights(t *testing.T) {
	for height := 1; height <= 7; height++ {
		model, _ := newTestModel("/tmp", heikou.BackendCodex)
		model.width, model.height = 40, height
		assertViewFits(t, model.View().Content, model.width, model.height)
	}
}

func TestNewWithSelectedSessionRestoresGuideAfterInitialSnapshot(t *testing.T) {
	session := testDurableSession("018f0000-0000-4000-8000-000000000041", "", heikou.BackendClaude, "guided tour", "/tmp", time.Now())
	controller := &fakeController{snapshot: control.Snapshot{Sessions: []control.Session{session}, StatePath: "/tmp/heikou-test-state.json"}}
	model := NewWithSelectedSession(controller, "/tmp", heikou.BackendClaude, config.Store{}, config.Default(), session.ID)
	model.setSnapshot(controller.snapshot)
	model.restoreSelection()
	selected, ok := model.selectedSession()
	if !ok || selected.ID != session.ID {
		t.Fatalf("selected session = (%q, %v), want %q", selected.ID, ok, session.ID)
	}
}

func TestSessionRowKeepsRuntimeVisibleAtEightyColumns(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width = 80
	session := testDurableSession("018f0000-0000-4000-8000-000000000003", "", heikou.BackendCodex, "a task long enough to compete with the runtime column for space", "/tmp", time.Now())
	session.Runtime.StartedAt = time.Now().Add(-2 * time.Minute)
	row := model.renderSessionRow(session, false)
	if !strings.Contains(ansi.Strip(row), "2m") {
		t.Fatalf("runtime was truncated from row: %q", ansi.Strip(row))
	}
	if width := ansi.StringWidth(row); width != 80 {
		t.Fatalf("row width = %d, want 80", width)
	}
}

func TestNarrowSessionRowsPreserveStatusAndTitleBeforeMetadata(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	session := testDurableSession("018f0000-0000-4000-8000-000000000003", "", heikou.BackendCodex, "initial task", "/tmp", time.Now())
	session.Record.Title = "Release Linux build"

	model.width = 40
	for name, row := range map[string]string{
		"dashboard": model.renderSessionRow(session, false),
		"organizer": model.renderOrganizerSessionRow(session, false, false),
	} {
		plain := ansi.Strip(row)
		if width := ansi.StringWidth(row); width != model.width {
			t.Errorf("%s row width = %d, want %d: %q", name, width, model.width, plain)
		}
		if !strings.Contains(plain, "live") || !strings.Contains(plain, session.Record.Title) {
			t.Errorf("%s row lost status or title at 40 columns: %q", name, plain)
		}
		if strings.Contains(plain, "codex") || strings.Contains(plain, shortID(session.ID)) {
			t.Errorf("%s row retained lower-priority metadata ahead of the title: %q", name, plain)
		}
	}
}

func TestMediumSessionRowsRestoreRunnerWithoutCrowdingOutTitle(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width = sessionRowRunnerMinWidth
	session := testDurableSession("018f0000-0000-4000-8000-000000000003", "", heikou.BackendCodex, "initial task", "/tmp", time.Now())
	session.Record.Title = "Release Linux build"

	for name, row := range map[string]string{
		"dashboard": model.renderSessionRow(session, false),
		"organizer": model.renderOrganizerSessionRow(session, false, false),
	} {
		plain := ansi.Strip(row)
		if width := ansi.StringWidth(row); width != model.width {
			t.Errorf("%s row width = %d, want %d: %q", name, width, model.width, plain)
		}
		if !strings.Contains(plain, "codex") || !strings.Contains(plain, session.Record.Title) {
			t.Errorf("%s row did not restore runner alongside the title: %q", name, plain)
		}
		if strings.Contains(plain, shortID(session.ID)) {
			t.Errorf("%s row restored the short ID before the rich-layout threshold: %q", name, plain)
		}
	}
}

// TestWideRowsStayInsideThePane covers the rich layout, which the narrow and
// medium cases above never reach because both run below
// sessionRowRichMinWidth. The unselected wide organizer row used to compute its
// truncation into a variable and then return a string built without it, so a
// long title overflowed the pane by the columns its fixed-prefix budget
// under-counts.
func TestWideRowsStayInsideThePane(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	session := testDurableSession("018f0000-0000-4000-8000-000000000005", "", heikou.BackendNoAgent, "initial task", "/tmp", time.Now())
	session.Record.Title = strings.Repeat("a very long title ", 12)

	for _, width := range []int{sessionRowRichMinWidth, 80, 120} {
		model.width = width
		for _, selected := range []bool{false, true} {
			for _, source := range []bool{false, true} {
				row := model.renderOrganizerSessionRow(session, selected, source)
				if got := ansi.StringWidth(row); got != width {
					t.Errorf("organizer row width = %d, want %d (selected=%v, source=%v): %q",
						got, width, selected, source, ansi.Strip(row))
				}
			}
			row := model.renderSessionRow(session, selected)
			if got := ansi.StringWidth(row); got != width {
				t.Errorf("dashboard row width = %d, want %d (selected=%v): %q",
					got, width, selected, ansi.Strip(row))
			}
		}
	}
}

func TestSelectedRowHasExplicitMarkerAndContinuousWidth(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width = 80
	session := testDurableSession("018f0000-0000-4000-8000-000000000004", "", heikou.BackendNoAgent, "scratch shell", "/tmp", time.Now())
	selected := model.renderSessionRow(session, true)
	unselected := model.renderSessionRow(session, false)
	if !strings.HasPrefix(ansi.Strip(selected), "›") {
		t.Fatalf("selected row has no explicit marker: %q", ansi.Strip(selected))
	}
	if strings.HasPrefix(ansi.Strip(unselected), "›") {
		t.Fatalf("unselected row has selected marker: %q", ansi.Strip(unselected))
	}
	if width := ansi.StringWidth(selected); width != 80 {
		t.Fatalf("selected row width = %d, want 80", width)
	}
}

func TestSessionViewsRenderTitleBeforeLatestMessageSentThroughHeikou(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 160, 30
	session := testDurableSession("018f0000-0000-4000-8000-000000000005", "", heikou.BackendCodex, "initial task", "/tmp", time.Now())
	session.Record.Title = "release linux build"
	session.LastUserMessage = "most recent follow-up"
	model.snapshot.Sessions = []control.Session{session}
	model.setSnapshot(model.snapshot)
	model.selected = sessionRowKey(session)
	model.restoreSelection()
	model.previewID = session.ID

	row := ansi.Strip(model.renderSessionRow(session, false))
	if title, latest := strings.Index(row, "release linux build"), strings.Index(row, "latest via Heikou · most recent follow-up"); title < 0 || latest <= title {
		t.Fatalf("dashboard row did not render title before latest detail: %q", row)
	}
	details := ansi.Strip(model.renderDetails())
	if !strings.Contains(details, "title release linux build") || !strings.Contains(details, "latest via Heikou · most recent follow-up") ||
		!strings.Contains(details, "initial task · initial task") {
		t.Fatalf("details did not show title and latest user message: %q", details)
	}
	organizer := ansi.Strip(model.renderOrganizerSessionRow(session, false, false))
	if title, latest := strings.Index(organizer, "release linux build"), strings.Index(organizer, "latest via Heikou · most recent follow-up"); title < 0 || latest <= title {
		t.Fatalf("organizer row did not render title before latest detail: %q", organizer)
	}
}

func TestStatusLabelDistinguishesUnknownExitFromKnownSuccess(t *testing.T) {
	zero, seven := 0, 7
	tests := []struct {
		name string
		code *int
		want string
	}{
		{name: "unknown", code: nil, want: "exited ?"},
		{name: "success", code: &zero, want: "exited"},
		{name: "failure", code: &seven, want: "failed 7"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := heikou.Session{Status: heikou.StatusExited, ExitCode: test.code}
			if test.code != nil && *test.code != 0 {
				runtime.Status = heikou.StatusFailed
			}
			session := control.Session{Status: control.StatusExited, Runtime: &runtime}
			_, got := statusLabel(session)
			if got != test.want {
				t.Fatalf("statusLabel() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRowsGroupDurableSessionsAndKeepOrphansSeparate(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	container := testWorkstream("018f0000-0000-4000-8000-000000000010", "Core", []string{"/tmp"}, now)
	member := testDurableSession("018f0000-0000-4000-8000-000000000011", container.ID, heikou.BackendCodex, "member", "/tmp", now)
	ungrouped := testDurableSession("018f0000-0000-4000-8000-000000000012", "", heikou.BackendClaude, "inbox", "/tmp", now)
	orphan := testOrphan("018f0000-0000-4000-8000-000000000013", "/tmp", now)
	model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{container}, Sessions: []control.Session{member, ungrouped}, Orphans: []control.Session{orphan}})
	rows := model.rows()
	want := []string{workstreamRowKey(container.ID), sessionRowKey(member), ungroupedKey, sessionRowKey(ungrouped), orphanedKey, sessionRowKey(orphan)}
	if len(rows) != len(want) {
		t.Fatalf("rows = %#v", rows)
	}
	for index := range want {
		if rows[index].key != want[index] {
			t.Fatalf("row[%d] = %q, want %q", index, rows[index].key, want[index])
		}
	}
	model.collapsed[workstreamRowKey(container.ID)] = true
	for _, row := range model.rows() {
		if row.sessionID == member.ID {
			t.Fatal("collapsed workstream still rendered its member")
		}
	}
}

func TestInitialSelectionRemainsUngroupedForBackwardCompatibleLaunches(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.snapshot.Workstreams = []workstream.Workstream{testWorkstream("018f0000-0000-4000-8000-000000000014", "Core", []string{"/tmp/core"}, time.Now())}
	model.setSnapshot(model.snapshot)
	model.restoreSelection()
	if model.selected != ungroupedKey || model.launchWorkstreamID() != "" || model.launchRoot() != "/tmp" {
		t.Fatalf("initial launch context = selected %q workstream %q root %q", model.selected, model.launchWorkstreamID(), model.launchRoot())
	}
}

func TestSelectedWorkstreamAndRootDriveLaunch(t *testing.T) {
	model, controller := newTestModel("/tmp/dashboard", heikou.BackendCodex)
	model.width, model.height = 100, 30
	now := time.Now()
	container := testWorkstream("018f0000-0000-4000-8000-000000000020", "Multi repo", []string{"/tmp/api", "/tmp/web"}, now)
	model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{container}})
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	model = updated.(Model)
	if got := model.launchRoot(); got != "/tmp/web" {
		t.Fatalf("launch root = %q, want /tmp/web", got)
	}
	model.insertText("build the web")
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("typed Enter did not produce a start command")
	}
	_ = cmd()
	if controller.startRequest.WorkstreamID != container.ID || controller.startRequest.Root != "/tmp/web" {
		t.Fatalf("start request = %#v", controller.startRequest)
	}
	plain := ansi.Strip(model.View().Content)
	if !strings.Contains(plain, "Multi repo") || !strings.Contains(plain, "/tmp/web") {
		t.Fatalf("launch context is not visible:\n%s", plain)
	}
}

func TestEmptyEnterOnHeaderCollapsesInsteadOfAttaching(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	container := testWorkstream("018f0000-0000-4000-8000-000000000030", "Core", []string{"/tmp"}, time.Now())
	model.snapshot.Workstreams = []workstream.Workstream{container}
	model.setSnapshot(model.snapshot)
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(Model)
	if cmd != nil || !model.collapsed[workstreamRowKey(container.ID)] {
		t.Fatal("header Enter did not toggle collapse locally")
	}
}

func TestEmptyTabCyclesThroughNoAgent(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	for _, want := range []heikou.Backend{heikou.BackendClaude, heikou.BackendNoAgent, heikou.BackendCodex} {
		updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
		model = updated.(Model)
		if model.backend != want {
			t.Fatalf("backend = %q, want %q", model.backend, want)
		}
	}
}

func TestSettingsShortcutDoesNotStealPrintableS(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 's', Text: "s"}))
	model = updated.(Model)
	if model.screen == screenSettings || model.inputValue() != "s" {
		t.Fatalf("printable s changed mode/input: screen=%v input=%q", model.screen, model.inputValue())
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 's', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if model.screen != screenSettings {
		t.Fatal("Ctrl-S did not open settings")
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if updated.(Model).screen != screenDashboard {
		t.Fatal("Escape did not return to dashboard")
	}
}

func TestF3OpensOrganizerWithoutStealingPrintableInput(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyF3}))
	model = updated.(Model)
	if model.screen != screenOrganizer {
		t.Fatal("F3 did not open workstreams")
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 'n', Text: "n"}))
	model = updated.(Model)
	if model.organizerEdit != organizerEditCreate {
		t.Fatal("n did not begin workstream creation")
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 'N', Text: "N", Mod: tea.ModShift}))
	if updated.(Model).organizerValue() != "N" {
		t.Fatal("organizer editor did not accept text")
	}
}

func TestTypedScreenAndHelpOverlayReturnToUnderlyingScreen(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 's', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if model.screen != screenSettings || model.overlay != overlayNone {
		t.Fatalf("settings navigation = screen %v overlay %v", model.screen, model.overlay)
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyF1}))
	model = updated.(Model)
	if model.screen != screenSettings || model.overlay != overlayHelp {
		t.Fatalf("settings help = screen %v overlay %v", model.screen, model.overlay)
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(Model)
	if model.screen != screenSettings || model.overlay != overlayNone {
		t.Fatalf("closing settings help = screen %v overlay %v", model.screen, model.overlay)
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyF3}))
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyF1}))
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(Model)
	if model.screen != screenOrganizer || model.overlay != overlayNone {
		t.Fatalf("closing organizer help = screen %v overlay %v", model.screen, model.overlay)
	}
}

func TestOrganizerRenameKeyEditsAndClearsSessionTitle(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	container := testWorkstream("018f0000-0000-4000-8000-000000000020", "Core", []string{"/tmp"}, now)
	session := testDurableSession("018f0000-0000-4000-8000-000000000021", container.ID, heikou.BackendCodex, "initial task", "/tmp", now)
	session.Record.Title = "Old title"
	model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{container}, Sessions: []control.Session{session}})
	model.selected = sessionRowKey(session)
	model.restoreSelection()
	model.openOrganizer()
	model.selectOrganizerKey(sessionRowKey(session))

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	model = updated.(Model)
	if model.organizerEdit != organizerEditSessionTitle || model.organizerValue() != "Old title" {
		t.Fatalf("session title edit = mode %v value %q", model.organizerEdit, model.organizerValue())
	}
	model.organizerInput = splitGraphemes("Release Linux build")
	model.organizerInputCursor = len(model.organizerInput)
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("saving a session title did not produce a command")
	}
	message := cmd()
	if controller.titledSession != session.ID || controller.titleValue != "Release Linux build" {
		t.Fatalf("title command = session %q title %q", controller.titledSession, controller.titleValue)
	}
	updated, _ = model.Update(message)
	model = updated.(Model)

	session.Record.Title = "Release Linux build"
	model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{container}, Sessions: []control.Session{session}})
	model.selectOrganizerKey(sessionRowKey(session))
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	model = updated.(Model)
	model.organizerInput, model.organizerInputCursor = nil, 0
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd == nil {
		t.Fatal("clearing a session title did not produce a command")
	}
	_ = updated
	_ = cmd()
	if controller.titledSession != session.ID || controller.titleValue != "" {
		t.Fatalf("clear title command = session %q title %q", controller.titledSession, controller.titleValue)
	}

	model.organizerEdit = organizerEditNone
	model.selectOrganizerKey(workstreamRowKey(container.ID))
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	if updated.(Model).organizerEdit != organizerEditWorkstreamName {
		t.Fatal("r on a workstream did not retain workstream rename behavior")
	}
}

func TestOrganizerShowsExpandableSessionTree(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	container := testWorkstream("018f0000-0000-4000-8000-000000000034", "Core", []string{"/tmp/core"}, now)
	member := testDurableSession("018f0000-0000-4000-8000-000000000035", container.ID, heikou.BackendCodex, "member task", "/tmp/core", now)
	ungrouped := testDurableSession("018f0000-0000-4000-8000-000000000036", "", heikou.BackendClaude, "inbox task", "/tmp", now)
	orphan := testOrphan("018f0000-0000-4000-8000-000000000037", "/tmp", now)
	model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{container}, Sessions: []control.Session{member, ungrouped}, Orphans: []control.Session{orphan}})
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()
	model.openOrganizer()

	want := []string{workstreamRowKey(container.ID), sessionRowKey(member), ungroupedKey, sessionRowKey(ungrouped), orphanedKey, sessionRowKey(orphan)}
	rows := model.organizerRows()
	if len(rows) != len(want) {
		t.Fatalf("organizer rows = %#v", rows)
	}
	for index, key := range want {
		if rows[index].key != key {
			t.Fatalf("organizer row[%d] = %q, want %q", index, rows[index].key, key)
		}
	}

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	model = updated.(Model)
	for _, row := range model.organizerRows() {
		if row.sessionID == member.ID {
			t.Fatal("collapsed organizer group still includes its session")
		}
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	model = updated.(Model)
	if _, found := findOrganizerRow(model.organizerRows(), sessionRowKey(member)); !found {
		t.Fatal("expanded organizer group did not restore its session")
	}
}

func TestOrganizerReordersOnlyNamedWorkstreams(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	first := testWorkstream("018f0000-0000-4000-8000-000000000061", "First", []string{"/tmp"}, now)
	second := testWorkstream("018f0000-0000-4000-8000-000000000062", "Second", []string{"/tmp"}, now)
	model.snapshot.Workstreams = []workstream.Workstream{first, second}
	model.setSnapshot(model.snapshot)
	model.selected = workstreamRowKey(second.ID)
	model.restoreSelection()
	model.openOrganizer()

	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp, Mod: tea.ModShift}))
	model = updated.(Model)
	if cmd == nil || !model.busy {
		t.Fatal("Shift-Up did not start a workstream reorder")
	}
	message := cmd()
	if controller.reorderedWorkstream != second.ID || controller.reorderedDelta != -1 {
		t.Fatalf("reorder = (%q, %d), want (%q, -1)", controller.reorderedWorkstream, controller.reorderedDelta, second.ID)
	}
	updated, _ = model.Update(message)
	model = updated.(Model)
	if model.organizerSelected != workstreamRowKey(second.ID) {
		t.Fatalf("reorder lost selected workstream: %q", model.organizerSelected)
	}

	model.busy = false
	model.selectOrganizerKey(workstreamRowKey(first.ID))
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp, Mod: tea.ModShift}))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("locally first workstream did not reach the authoritative controller")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if !strings.Contains(model.notice, "moved workstream up") {
		t.Fatalf("authoritative stale-snapshot move notice = %q", model.notice)
	}

	model.busy = false
	controller.reorderNoop = true
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp, Mod: tea.ModShift}))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("boundary reorder did not reach the authoritative controller")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if !strings.Contains(model.notice, "already first") {
		t.Fatalf("authoritative boundary notice = %q", model.notice)
	}
	controller.reorderNoop = false
	model.busy = false
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown, Mod: tea.ModShift}))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("Shift-Down did not start a workstream reorder")
	}
	_ = cmd()
	if controller.reorderedWorkstream != first.ID || controller.reorderedDelta != 1 {
		t.Fatalf("down reorder = (%q, %d), want (%q, 1)", controller.reorderedWorkstream, controller.reorderedDelta, first.ID)
	}
	model.busy = false
	model.selectOrganizerKey(ungroupedKey)
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown, Mod: tea.ModShift}))
	model = updated.(Model)
	if cmd != nil || !strings.Contains(model.errorText, "named workstream") {
		t.Fatalf("synthetic reorder = command %v, error %q", cmd != nil, model.errorText)
	}
}

func TestResizeModeAdjustsDashboardAndOrganizerIndependently(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 100, 30
	container := testWorkstream("018f0000-0000-4000-8000-000000000063", "Core", []string{"/tmp"}, time.Now())
	model.snapshot.Workstreams = []workstream.Workstream{container}
	model.setSnapshot(model.snapshot)
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()
	selected := model.selected
	baseDetails, baseList := model.detailHeight(), model.listHeight()

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if !model.resizeMode || !strings.Contains(ansi.Strip(model.renderHeader()), "RESIZE") {
		t.Fatal("Ctrl-G did not enter visible dashboard resize mode")
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	model = updated.(Model)
	if model.detailHeight() != baseDetails+1 || model.listHeight() != baseList-1 {
		t.Fatalf("dashboard resize = details %d list %d, want %d/%d", model.detailHeight(), model.listHeight(), baseDetails+1, baseList-1)
	}
	if model.selected != selected || model.inputValue() != "" {
		t.Fatal("dashboard resize mutated selection or composer")
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	model = updated.(Model)
	if model.detailHeight() != baseDetails || model.listHeight() != baseList {
		t.Fatal("resize reset did not restore automatic dashboard sizing")
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(Model)
	if model.resizeMode {
		t.Fatal("Escape did not close resize mode")
	}

	model.openOrganizer()
	baseContext, baseViewport := model.organizerContextHeight(), model.organizerViewportHeight()
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	model = updated.(Model)
	if model.organizerContextHeight() != baseContext+1 || model.organizerViewportHeight() != baseViewport-1 {
		t.Fatalf("organizer resize = context %d viewport %d, want %d/%d", model.organizerContextHeight(), model.organizerViewportHeight(), baseContext+1, baseViewport-1)
	}
	if model.detailHeight() != baseDetails {
		t.Fatal("organizer resize changed dashboard detail sizing")
	}
}

func TestResizeModeClampsSafelyAcrossTerminalAndComposerSizes(t *testing.T) {
	for _, size := range []struct{ width, height int }{{20, 8}, {80, 24}, {120, 50}} {
		model, _ := newTestModel("/tmp", heikou.BackendCodex)
		model.width, model.height = size.width, size.height
		model.resizeMode = true
		for range 100 {
			model.resizeLowerPane(1)
		}
		if model.detailHeight() < 0 || model.listHeight() < 2 {
			t.Fatalf("grown dashboard split at %dx%d = list %d detail %d", size.width, size.height, model.listHeight(), model.detailHeight())
		}
		for range 200 {
			model.resizeLowerPane(-1)
		}
		if model.detailHeight() != 0 {
			t.Fatalf("shrunk dashboard detail at %dx%d = %d, want 0", size.width, size.height, model.detailHeight())
		}
		model.insertText("one\ntwo\nthree\nfour\nfive")
		assertViewFits(t, model.View().Content, size.width, size.height)

		model.resizeMode = false
		model.openOrganizer()
		for range 100 {
			model.resizeLowerPane(1)
		}
		if available := model.organizerAvailableHeight(); available >= 3 && model.organizerViewportHeight() < 3 {
			t.Fatalf("grown organizer consumed session viewport at %dx%d: context %d viewport %d", size.width, size.height, model.organizerContextHeight(), model.organizerViewportHeight())
		}
		for range 200 {
			model.resizeLowerPane(-1)
		}
		if model.organizerContextHeight() != 0 {
			t.Fatalf("shrunk organizer context at %dx%d = %d, want 0", size.width, size.height, model.organizerContextHeight())
		}
		assertViewFits(t, model.View().Content, size.width, size.height)
	}
}

func TestResizeUsesVisibleHeightAfterTerminalShrinks(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 100, 50
	for range 100 {
		model.resizeLowerPane(1)
	}
	model.height = 24
	before := model.detailHeight()
	model.resizeLowerPane(-1)
	if got, want := model.detailHeight(), max(0, before-1); got != want {
		t.Fatalf("dashboard resize after terminal shrink = %d, want %d", got, want)
	}

	model.openOrganizer()
	model.height = 50
	for range 100 {
		model.resizeLowerPane(1)
	}
	model.height = 24
	before = model.organizerContextHeight()
	model.resizeLowerPane(-1)
	if got, want := model.organizerContextHeight(), max(0, before-1); got != want {
		t.Fatalf("organizer resize after terminal shrink = %d, want %d", got, want)
	}
}

func TestResizeUsesVisibleHeightAfterComposerExpands(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 100, 30
	for range 100 {
		model.resizeLowerPane(1)
	}
	model.insertText("one\ntwo\nthree\nfour\nfive")
	before := model.detailHeight()
	model.resizeLowerPane(-1)
	if got, want := model.detailHeight(), max(0, before-1); got != want {
		t.Fatalf("dashboard resize after composer growth = %d, want %d", got, want)
	}
}

func TestNonLayoutKeyLeavesResizeModeAndKeepsItsMeaning(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.resizeMode = true
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	model = updated.(Model)
	if model.resizeMode || model.inputValue() != "x" {
		t.Fatalf("printable key after resize = mode %v input %q", model.resizeMode, model.inputValue())
	}

	model.clearInput()
	model.resizeMode = true
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}))
	model = updated.(Model)
	if model.resizeMode || model.inputValue() != "\n" {
		t.Fatalf("Shift-Enter after resize = mode %v input %q", model.resizeMode, model.inputValue())
	}
}

func TestOrganizerEditsAndRemovesSelectedRoot(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	container := testWorkstream("018f0000-0000-4000-8000-000000000033", "Core", []string{"/tmp/api", "/tmp/web"}, time.Now())
	model.snapshot.Workstreams = []workstream.Workstream{container}
	model.setSnapshot(model.snapshot)
	model.selected = workstreamRowKey(container.ID)
	model.rootIndex[container.ID] = 1
	model.openOrganizer()

	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: 'P', Text: "P", Mod: tea.ModShift}))
	model = updated.(Model)
	if cmd != nil || model.organizerEdit != organizerEditReplaceRoot || model.organizerRootTarget != "/tmp/web" {
		t.Fatalf("Shift-P did not edit selected root: mode=%v target=%q", model.organizerEdit, model.organizerRootTarget)
	}
	model.organizerInput = splitGraphemes("/tmp/frontend")
	model.organizerInputCursor = len(model.organizerInput)
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd == nil {
		t.Fatal("root edit did not produce a command")
	}
	_ = updated
	_ = cmd()
	if controller.replacedRootWorkstream != container.ID || controller.replacedRootCurrent != "/tmp/web" || controller.replacedRootValue != "/tmp/frontend" {
		t.Fatalf("root edit = workstream %q current %q replacement %q", controller.replacedRootWorkstream, controller.replacedRootCurrent, controller.replacedRootValue)
	}

	model, controller = newTestModel("/tmp", heikou.BackendCodex)
	model.snapshot.Workstreams = []workstream.Workstream{container}
	model.setSnapshot(model.snapshot)
	model.selected = workstreamRowKey(container.ID)
	model.rootIndex[container.ID] = 1
	model.openOrganizer()
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	model = updated.(Model)
	if cmd != nil || model.confirmRootRemoval == "" {
		t.Fatal("first d did not arm root removal")
	}
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	if cmd == nil {
		t.Fatal("second d did not produce root removal command")
	}
	_ = updated
	_ = cmd()
	if controller.removedRootWorkstream != container.ID || controller.removedRootValue != "/tmp/web" {
		t.Fatalf("root removal = workstream %q root %q", controller.removedRootWorkstream, controller.removedRootValue)
	}
}

func TestOrganizerRootRemovalConfirmationTracksSelectedRoot(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	container := testWorkstream("018f0000-0000-4000-8000-000000000032", "Core", []string{"/tmp/api", "/tmp/web"}, time.Now())
	model.snapshot.Workstreams = []workstream.Workstream{container}
	model.setSnapshot(model.snapshot)
	model.selected = workstreamRowKey(container.ID)
	model.openOrganizer()

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	model = updated.(Model)
	if cmd != nil || controller.removedRootValue != "" {
		t.Fatal("a confirmation armed for another root removed the newly selected root")
	}
	if !strings.Contains(model.notice, "root web") {
		t.Fatalf("new root was not armed explicitly: %q", model.notice)
	}
}

func TestOrganizerHelpClearsRootRemovalConfirmation(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	container := testWorkstream("018f0000-0000-4000-8000-000000000031", "Core", []string{"/tmp/api", "/tmp/web"}, time.Now())
	model.snapshot.Workstreams = []workstream.Workstream{container}
	model.setSnapshot(model.snapshot)
	model.selected = workstreamRowKey(container.ID)
	model.openOrganizer()

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	model = updated.(Model)
	if model.confirmRootRemoval == "" {
		t.Fatal("first d did not arm root removal")
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyF1}))
	model = updated.(Model)
	if model.overlay != overlayHelp || model.confirmRootRemoval != "" {
		t.Fatalf("help did not clear root confirmation: overlay=%v confirmation=%q", model.overlay, model.confirmRootRemoval)
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	model = updated.(Model)
	if cmd != nil || model.confirmRootRemoval == "" || controller.removedRootValue != "" {
		t.Fatal("root removal executed without a fresh post-help confirmation")
	}
}

func TestOrganizerMovesDurableSessionWithoutClosing(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	core := testWorkstream("018f0000-0000-4000-8000-000000000038", "Core", []string{"/tmp/core"}, now)
	web := testWorkstream("018f0000-0000-4000-8000-000000000039", "Web", []string{"/tmp/web"}, now)
	member := testDurableSession("018f0000-0000-4000-8000-00000000003a", core.ID, heikou.BackendCodex, "member", "/tmp/core", now)
	model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{core, web}, Sessions: []control.Session{member}})
	model.selected = sessionRowKey(member)
	model.restoreSelection()
	model.openOrganizer()
	model.selectOrganizerKey(workstreamRowKey(web.ID))

	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("destination Enter did not produce a move command")
	}
	message := cmd()
	if controller.movedSession != member.ID || controller.movedTarget != web.ID {
		t.Fatalf("move = session %q target %q", controller.movedSession, controller.movedTarget)
	}
	updated, _ = model.Update(message)
	model = updated.(Model)
	if model.screen != screenOrganizer || model.organizerSource != "" || model.organizerSelected != workstreamRowKey(web.ID) {
		t.Fatalf("organizer did not remain ready after move: screen=%v source=%q selected=%q", model.screen, model.organizerSource, model.organizerSelected)
	}
}

func TestOrganizerKeepsSelectedGuideMarkedThroughWorkstreamCreation(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendClaude)
	now := time.Now()
	guide := testDurableSession("018f0000-0000-4000-8000-000000000073", "", heikou.BackendClaude, "guided tour", "/tmp", now)
	destination := testWorkstream("018f0000-0000-4000-8000-000000000074", "First project", []string{"/tmp"}, now)
	model.snapshot.Sessions = []control.Session{guide}
	model.setSnapshot(model.snapshot)
	model.selected = sessionRowKey(guide)
	model.restoreSelection()
	model.openOrganizer()
	if model.organizerSource != guide.ID {
		t.Fatalf("opening organizer source = %q, want guide %q", model.organizerSource, guide.ID)
	}

	updated, _ := model.Update(workstreamMsg{action: "create", item: destination})
	model = updated.(Model)
	model.snapshot.Workstreams = []workstream.Workstream{destination}
	model.setSnapshot(model.snapshot)
	model.restoreOrganizerSelection()
	if model.organizerSource != guide.ID || model.organizerSelected != workstreamRowKey(destination.ID) {
		t.Fatalf("post-create source=%q selected=%q", model.organizerSource, model.organizerSelected)
	}

	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("Enter on the new workstream did not move the guided session")
	}
	_ = cmd()
	if controller.movedSession != guide.ID || controller.movedTarget != destination.ID {
		t.Fatalf("guided move = session %q target %q", controller.movedSession, controller.movedTarget)
	}
}

func TestOrganizerUseReturnsWithSessionSelected(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	container := testWorkstream("018f0000-0000-4000-8000-00000000003c", "Core", []string{"/tmp/api", "/tmp/web"}, now)
	member := testDurableSession("018f0000-0000-4000-8000-00000000003d", container.ID, heikou.BackendCodex, "member", "/tmp/web", now)
	model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{container}, Sessions: []control.Session{member}})
	model.collapsed[workstreamRowKey(container.ID)] = true
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()
	model.openOrganizer()
	model.selectOrganizerKey(sessionRowKey(member))

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 'u', Text: "u"}))
	model = updated.(Model)
	if model.screen != screenDashboard || model.selected != sessionRowKey(member) || model.collapsed[workstreamRowKey(container.ID)] {
		t.Fatalf("use did not return with visible session selected: screen=%v selected=%q collapsed=%v", model.screen, model.selected, model.collapsed[workstreamRowKey(container.ID)])
	}
	if model.launchRoot() != "/tmp/web" {
		t.Fatalf("use did not align the launch root to selected session: %q", model.launchRoot())
	}
}

func TestOrganizerMoveExplicitlyAdoptsSelectedOrphan(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	orphan := testOrphan("018f0000-0000-4000-8000-00000000003b", "/tmp", time.Now())
	model.snapshot.Orphans = []control.Session{orphan}
	model.setSnapshot(model.snapshot)
	model.selected = sessionRowKey(orphan)
	model.restoreSelection()
	model.openOrganizer()
	if model.organizerSource != orphan.ID {
		t.Fatal("orphan was not retained as organizer source")
	}
	model.selectOrganizerKey(ungroupedKey)
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("move on orphan did not produce an adoption command")
	}
	_ = cmd()
	if controller.adoptSession != orphan.ID || controller.adoptTarget != "" {
		t.Fatalf("adoption = session %q target %q", controller.adoptSession, controller.adoptTarget)
	}
}

func TestSettingsAndOrganizerViewsStayWithinTerminal(t *testing.T) {
	for _, size := range []struct{ width, height int }{{1, 12}, {2, 12}, {8, 12}, {20, 12}, {40, 15}, {80, 24}, {120, 40}} {
		model, _ := newTestModel("/tmp", heikou.BackendCodex)
		model.width, model.height = size.width, size.height
		model.screen = screenSettings
		assertViewFits(t, model.View().Content, size.width, size.height)
		model.screen = screenOrganizer
		assertViewFits(t, model.View().Content, size.width, size.height)
		model.screen, model.overlay = screenDashboard, overlayHelp
		assertViewFits(t, model.View().Content, size.width, size.height)
	}
}

func TestSelectedOrganizerRowsRemainValidAtNarrowWidths(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	container := testWorkstream("018f0000-0000-4000-8000-000000000053", "Narrow", []string{"/tmp"}, now)
	session := testDurableSession("018f0000-0000-4000-8000-000000000054", container.ID, heikou.BackendCodex, "narrow row", "/tmp", now)
	model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{container}, Sessions: []control.Session{session}})
	model.height = 12
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()
	model.openOrganizer()

	for width := 1; width <= 20; width++ {
		model.width = width
		model.selectOrganizerKey(workstreamRowKey(container.ID))
		assertViewFits(t, model.View().Content, width, model.height)
		model.selectOrganizerKey(sessionRowKey(session))
		assertViewFits(t, model.View().Content, width, model.height)
	}
}

func TestTopBarNamesEveryView(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 100, 30

	assertMode := func(name string, content string) {
		t.Helper()
		first := strings.Split(ansi.Strip(content), "\n")[0]
		if !strings.Contains(first, name) {
			t.Fatalf("top bar %q does not name mode %q", first, name)
		}
	}
	assertMode("DASHBOARD", model.View().Content)
	model.screen = screenOrganizer
	assertMode("WORKSTREAM ORGANIZER", model.View().Content)
	model.screen = screenSettings
	assertMode("SETTINGS", model.View().Content)
	model.screen, model.overlay = screenDashboard, overlayHelp
	assertMode("HELP", model.View().Content)
}

func TestOrganizerContextShowsWorkstreamNotesAndArtifactTree(t *testing.T) {
	artifactDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(artifactDir, "notes.md"), []byte("Ship the smallest useful version.\nThen dogfood it."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(artifactDir, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, "docs", "plan.md"), []byte("plan"), 0o600); err != nil {
		t.Fatal(err)
	}

	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 100, 30
	container := testWorkstream("018f0000-0000-4000-8000-000000000054", "Payments", []string{"/tmp"}, time.Now())
	container.ArtifactDir = artifactDir
	session := testDurableSession("018f0000-0000-4000-8000-000000000055", container.ID, heikou.BackendCodex, "member", "/tmp", time.Now())
	model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{container}, Sessions: []control.Session{session}})
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()
	model.openOrganizer()

	cmd := model.organizerContextCmd(true)
	if cmd == nil {
		t.Fatal("opening a named workstream did not request artifact context")
	}
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	plain := ansi.Strip(model.renderOrganizer())
	for _, want := range []string{"CONTEXT · Payments", "NOTES", "Ship the smallest useful version.", "FILES", "docs/", "plan.md"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("organizer context is missing %q:\n%s", want, plain)
		}
	}

	model.selectOrganizerKey(sessionRowKey(session))
	if cmd := model.organizerContextCmd(false); cmd != nil {
		t.Fatal("moving to a session in the same workstream re-read artifact context")
	}
	assertViewFits(t, model.View().Content, model.width, model.height)
}

func TestOrganizerExplicitContextRefreshReadsExternalChanges(t *testing.T) {
	artifactDir := t.TempDir()
	notesPath := filepath.Join(artifactDir, "notes.md")
	if err := os.WriteFile(notesPath, []byte("before manager edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	container := testWorkstream("018f0000-0000-4000-8000-000000000022", "Refresh", []string{"/tmp"}, time.Now())
	container.ArtifactDir = artifactDir
	model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{container}})
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()
	model.openOrganizer()
	initial := model.organizerContextCmd(true)
	if initial == nil {
		t.Fatal("initial context read was not requested")
	}
	updated, _ := model.Update(initial())
	model = updated.(Model)
	if model.organizerContext.snapshot.Notes != "before manager edit" {
		t.Fatalf("initial notes = %q", model.organizerContext.snapshot.Notes)
	}
	if err := os.WriteFile(notesPath, []byte("after manager edit"), 0o600); err != nil {
		t.Fatal(err)
	}

	updated, refresh := model.Update(tea.KeyPressMsg(tea.Key{Code: 'R', Text: "R", Mod: tea.ModShift}))
	model = updated.(Model)
	if refresh == nil {
		t.Fatal("R did not force an artifact context refresh")
	}
	updated, _ = model.Update(refresh())
	model = updated.(Model)
	if model.organizerContext.snapshot.Notes != "after manager edit" || model.notice != "workstream context refreshed" {
		t.Fatalf("refreshed notes = %q notice = %q", model.organizerContext.snapshot.Notes, model.notice)
	}
}

func TestLargeOrganizerContextGivesNotesAndTreeMoreRoom(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 100, 40
	container := testWorkstream("018f0000-0000-4000-8000-000000000064", "Deep context", []string{"/tmp"}, time.Now())
	model.snapshot.Workstreams = []workstream.Workstream{container}
	model.setSnapshot(model.snapshot)
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()
	model.openOrganizer()

	tree := make([]artifactTreeEntry, 12)
	for index := range tree {
		tree[index] = artifactTreeEntry{Name: "artifact-" + string(rune('a'+index)), Depth: 1, Regular: true}
	}
	model.organizerContext.snapshot = artifactContextSnapshot{
		WorkstreamID: container.ID,
		ArtifactDir:  container.ArtifactDir,
		Notes:        strings.Repeat("one useful note line\n", 12),
		NotesStatus:  artifactNotesReady,
		Tree:         tree,
	}
	height := model.organizerContextHeight()
	if height <= 12 {
		t.Fatalf("large default context height = %d, want more than the old 12-row cap", height)
	}
	lines := model.renderOrganizerContext(height)
	filesIndex := -1
	for index, line := range lines {
		if strings.Contains(ansi.Strip(line), "FILES") {
			filesIndex = index
			break
		}
	}
	if filesIndex <= 4 {
		t.Fatalf("notes received only %d rows, want more than the old three-row cap", filesIndex-1)
	}
	if treeRows := len(lines) - filesIndex - 1; treeRows <= 4 {
		t.Fatalf("artifact tree received only %d rows", treeRows)
	}
}

func TestOrganizerFooterExplainsEnterForSelectedNoun(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	container := testWorkstream("018f0000-0000-4000-8000-000000000056", "Core", []string{"/tmp"}, time.Now())
	session := testDurableSession("018f0000-0000-4000-8000-000000000057", container.ID, heikou.BackendCodex, "member", "/tmp", time.Now())
	model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{container}, Sessions: []control.Session{session}})
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()
	model.openOrganizer()

	if help := model.organizerHelp(); !strings.Contains(help, "Enter expand/collapse") ||
		!strings.Contains(help, "Shift-↑↓ reorder") || !strings.Contains(help, "Ctrl-G resize") {
		t.Fatalf("workstream footer = %q", help)
	}
	model.selectOrganizerKey(sessionRowKey(session))
	if help := model.organizerHelp(); !strings.Contains(help, "Enter mark for move") || !strings.Contains(help, "select on dashboard") {
		t.Fatalf("session footer = %q", help)
	}
	model.organizerSource = session.ID
	model.selectOrganizerKey(workstreamRowKey(container.ID))
	if help := model.organizerHelp(); !strings.Contains(help, "Enter move here") {
		t.Fatalf("move-destination footer = %q", help)
	}
}

func TestOrganizerContextIgnoresStaleSelectionRead(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	first := testWorkstream("018f0000-0000-4000-8000-000000000058", "First", []string{"/tmp"}, time.Now())
	first.ArtifactDir = t.TempDir()
	second := testWorkstream("018f0000-0000-4000-8000-000000000059", "Second", []string{"/tmp"}, time.Now())
	second.ArtifactDir = t.TempDir()
	model.snapshot.Workstreams = []workstream.Workstream{first, second}
	model.setSnapshot(model.snapshot)
	model.selected = workstreamRowKey(first.ID)
	model.restoreSelection()
	model.openOrganizer()

	firstCmd := model.organizerContextCmd(true)
	model.selectOrganizerKey(workstreamRowKey(second.ID))
	secondCmd := model.organizerContextCmd(false)
	if firstCmd == nil || secondCmd == nil {
		t.Fatal("selection changes did not produce distinct artifact reads")
	}
	updated, _ := model.Update(firstCmd())
	model = updated.(Model)
	if model.organizerContext.snapshot.WorkstreamID == first.ID {
		t.Fatal("stale first-workstream context replaced the current selection")
	}
	updated, _ = model.Update(secondCmd())
	model = updated.(Model)
	if model.organizerContext.snapshot.WorkstreamID != second.ID {
		t.Fatalf("current context = %q, want %q", model.organizerContext.snapshot.WorkstreamID, second.ID)
	}
}

func TestOrganizerContextRetriesSelectionWhoseStaleReadCompletedElsewhere(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	first := testWorkstream("018f0000-0000-4000-8000-00000000005a", "First", []string{"/tmp"}, time.Now())
	first.ArtifactDir = t.TempDir()
	second := testWorkstream("018f0000-0000-4000-8000-00000000005b", "Second", []string{"/tmp"}, time.Now())
	second.ArtifactDir = t.TempDir()
	model.snapshot.Workstreams = []workstream.Workstream{first, second}
	model.setSnapshot(model.snapshot)
	model.selected = workstreamRowKey(first.ID)
	model.restoreSelection()
	model.openOrganizer()

	firstCmd := model.organizerContextCmd(true)
	updated, _ := model.Update(firstCmd())
	model = updated.(Model)
	if model.organizerContext.snapshot.WorkstreamID != first.ID {
		t.Fatalf("cached context = %q, want %q", model.organizerContext.snapshot.WorkstreamID, first.ID)
	}

	model.selectOrganizerKey(workstreamRowKey(second.ID))
	staleSecondCmd := model.organizerContextCmd(false)
	if staleSecondCmd == nil {
		t.Fatal("selecting the second workstream did not begin a context read")
	}
	model.selectOrganizerKey(workstreamRowKey(first.ID))
	if cmd := model.organizerContextCmd(false); cmd != nil {
		t.Fatal("returning to cached first context unexpectedly re-read it")
	}
	if model.organizerContext.loadingKey != "" {
		t.Fatalf("abandoned second read remains marked loading: %q", model.organizerContext.loadingKey)
	}

	updated, _ = model.Update(staleSecondCmd())
	model = updated.(Model)
	model.selectOrganizerKey(workstreamRowKey(second.ID))
	retryCmd := model.organizerContextCmd(false)
	if retryCmd == nil {
		t.Fatal("stale second completion permanently suppressed its retry")
	}
	updated, _ = model.Update(retryCmd())
	model = updated.(Model)
	if model.organizerContext.snapshot.WorkstreamID != second.ID {
		t.Fatalf("retried context = %q, want %q", model.organizerContext.snapshot.WorkstreamID, second.ID)
	}
}

func TestSnapshotFetchesAreSingleFlightAndRejectLateOlderCompletion(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	firstCmd := model.requestSnapshot()
	if firstCmd == nil {
		t.Fatal("first snapshot request did not start")
	}
	firstGeneration := model.snapshotFetch.activeGeneration
	if cmd := model.requestSnapshot(); cmd != nil || !model.snapshotFetch.queued {
		t.Fatalf("overlapping snapshot request was not coalesced: cmd=%v queued=%v", cmd != nil, model.snapshotFetch.queued)
	}

	controller.snapshot = control.Snapshot{StatePath: "older"}
	firstResult := firstCmd()
	firstMessage, ok := firstResult.(snapshotMsg)
	if !ok {
		t.Fatalf("first snapshot command returned %T", firstResult)
	}
	updated, secondCmd := model.Update(firstMessage)
	model = updated.(Model)
	if secondCmd == nil || model.snapshotFetch.activeGeneration <= firstGeneration {
		t.Fatalf("queued snapshot did not start: generation=%d first=%d", model.snapshotFetch.activeGeneration, firstGeneration)
	}

	controller.snapshot = control.Snapshot{StatePath: "newer"}
	secondResult := secondCmd()
	secondMessage, ok := secondResult.(snapshotMsg)
	if !ok {
		t.Fatalf("second snapshot command returned %T", secondResult)
	}
	updated, _ = model.Update(secondMessage)
	model = updated.(Model)
	if model.snapshot.StatePath != "newer" {
		t.Fatalf("current snapshot = %q, want newer", model.snapshot.StatePath)
	}

	firstMessage.snapshot.StatePath = "late older"
	updated, _ = model.Update(firstMessage)
	model = updated.(Model)
	if model.snapshot.StatePath != "newer" {
		t.Fatalf("late older snapshot replaced newer state: %q", model.snapshot.StatePath)
	}
}

func TestPreviewFetchesAreSingleFlightAndRejectLateOlderCompletion(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	session := testDurableSession("018f0000-0000-4000-8000-00000000005c", "", heikou.BackendCodex, "preview", "/tmp", time.Now())
	model.snapshot.Sessions = []control.Session{session}
	model.setSnapshot(model.snapshot)
	model.selected = sessionRowKey(session)
	model.restoreSelection()

	controller.captureText = "older preview"
	firstCmd := model.requestPreview(session.ID)
	if firstCmd == nil {
		t.Fatal("first preview request did not start")
	}
	firstGeneration := model.previewFetch.activeGeneration
	if cmd := model.requestPreview(session.ID); cmd != nil || model.previewFetch.queuedID != session.ID {
		t.Fatalf("overlapping preview request was not coalesced: cmd=%v queued=%q", cmd != nil, model.previewFetch.queuedID)
	}
	firstResult := firstCmd()
	firstMessage, ok := firstResult.(previewMsg)
	if !ok {
		t.Fatalf("first preview command returned %T", firstResult)
	}
	updated, secondCmd := model.Update(firstMessage)
	model = updated.(Model)
	if secondCmd == nil || model.previewFetch.activeGeneration <= firstGeneration {
		t.Fatalf("queued preview did not start: generation=%d first=%d", model.previewFetch.activeGeneration, firstGeneration)
	}

	controller.captureText = "newer preview"
	secondResult := secondCmd()
	secondMessage, ok := secondResult.(previewMsg)
	if !ok {
		t.Fatalf("second preview command returned %T", secondResult)
	}
	updated, _ = model.Update(secondMessage)
	model = updated.(Model)
	if model.preview != "newer preview" {
		t.Fatalf("current preview = %q, want newer preview", model.preview)
	}

	firstMessage.text = "late older preview"
	updated, _ = model.Update(firstMessage)
	model = updated.(Model)
	if model.preview != "newer preview" {
		t.Fatalf("late older preview replaced newer output: %q", model.preview)
	}
}

func TestPreviewCompletionDoesNotReviveOutputForUnavailableSession(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	session := testDurableSession("018f0000-0000-4000-8000-00000000005d", "", heikou.BackendCodex, "preview", "/tmp", time.Now())
	model.snapshot.Sessions = []control.Session{session}
	model.setSnapshot(model.snapshot)
	model.selected = sessionRowKey(session)
	model.restoreSelection()
	controller.captureText = "stale live output"
	previewCmd := model.requestPreview(session.ID)
	if previewCmd == nil {
		t.Fatal("live session preview did not start")
	}

	unavailable := session
	unavailable.Runtime = nil
	unavailable.Status = control.StatusStopped
	controller.snapshot = control.Snapshot{Sessions: []control.Session{unavailable}}
	snapshotCmd := model.requestSnapshot()
	snapshotResult := snapshotCmd()
	snapshotMessage, ok := snapshotResult.(snapshotMsg)
	if !ok {
		t.Fatalf("snapshot command returned %T", snapshotResult)
	}
	updated, _ := model.Update(snapshotMessage)
	model = updated.(Model)
	if model.preview != "" || model.previewID != "" {
		t.Fatalf("unavailable snapshot retained preview %q for %q", model.preview, model.previewID)
	}

	previewResult := previewCmd()
	previewMessage, ok := previewResult.(previewMsg)
	if !ok {
		t.Fatalf("preview command returned %T", previewResult)
	}
	updated, _ = model.Update(previewMessage)
	model = updated.(Model)
	if model.preview != "" || model.previewID != "" {
		t.Fatalf("late preview revived unavailable output %q for %q", model.preview, model.previewID)
	}
}

func TestOrganizerViewportKeepsSelectedSessionVisible(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 60, 12
	now := time.Now()
	container := testWorkstream("018f0000-0000-4000-8000-000000000050", "Many", []string{"/tmp"}, now)
	model.snapshot.Workstreams = []workstream.Workstream{container}
	for index := 0; index < 18; index++ {
		id := "session-" + string(rune('a'+index))
		model.snapshot.Sessions = append(model.snapshot.Sessions, testDurableSession(id, container.ID, heikou.BackendNoAgent, "task "+id, "/tmp", now))
	}
	model.setSnapshot(model.snapshot)
	model.openOrganizer()
	last := model.snapshot.Sessions[len(model.snapshot.Sessions)-1]
	model.selectOrganizerKey(sessionRowKey(last))
	content := ansi.Strip(model.renderOrganizer())
	if !strings.Contains(content, "task "+last.ID) {
		t.Fatalf("selected session is outside organizer viewport:\n%s", content)
	}
	assertViewFits(t, model.View().Content, model.width, model.height)
}

func TestQuestionMarkHelpDoesNotStealComposerText(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 80, 24
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: '?', Text: "?"}))
	model = updated.(Model)
	if model.overlay != overlayHelp {
		t.Fatal("? on an empty composer did not open help")
	}
	plain := ansi.Strip(strings.Join(model.helpContentLines(), "\n"))
	for _, want := range []string{"local command center", "Nouns", "Workstream", "Orphaned", "Ctrl-G", "Shift-↑"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("help is missing %q:\n%s", want, plain)
		}
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(Model)
	model.insertText("why")
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: '?', Text: "?"}))
	model = updated.(Model)
	if model.overlay == overlayHelp || model.inputValue() != "why?" {
		t.Fatalf("printable ? changed mode/input: overlay=%v input=%q", model.overlay, model.inputValue())
	}
}

func TestHelpPanelScrolls(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height, model.overlay = 50, 12, overlayHelp
	before := ansi.Strip(model.renderHelp())
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown}))
	model = updated.(Model)
	after := ansi.Strip(model.renderHelp())
	if model.helpOffset == 0 || before == after {
		t.Fatal("PageDown did not scroll help")
	}
	assertViewFits(t, model.View().Content, model.width, model.height)
}

func TestOpeningHelpCancelsDestructiveConfirmations(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	session := testDurableSession("018f0000-0000-4000-8000-000000000054", "", heikou.BackendCodex, "finished", "/tmp", time.Now())
	session.Runtime = nil
	session.Status = control.StatusStopped
	model.snapshot.Sessions = []control.Session{session}
	model.setSnapshot(model.snapshot)
	model.selected = sessionRowKey(session)
	model.restoreSelection()
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 'x', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if model.confirmDelete != session.ID {
		t.Fatal("delete confirmation was not armed")
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyF1}))
	model = updated.(Model)
	if model.confirmDelete != "" || model.overlay != overlayHelp {
		t.Fatal("opening help did not cancel the destructive confirmation")
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: 'x', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if cmd != nil || model.confirmDelete != session.ID {
		t.Fatal("Ctrl-X after help did not require a fresh confirmation")
	}
}

func TestConfiguredComposerKeysDriveActions(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	model.settings.ComposerKeys = config.ComposerKeys{Reply: "ctrl+shift+n", CycleRunner: "f6", CycleRoot: "alt+r"}
	session := testDurableSession("018f0000-0000-4000-8000-000000000054", "", heikou.BackendCodex, "original", "/tmp", time.Now())
	model.snapshot.Sessions = []control.Session{session}
	model.setSnapshot(model.snapshot)
	model.selected = sessionRowKey(session)
	model.restoreSelection()

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	model = updated.(Model)
	if model.replyTarget != "" {
		t.Fatal("default Space still aimed the composer after rebinding")
	}
	if model.inputValue() != " " {
		t.Fatalf("rebound Space did not fall through to text: %q", model.inputValue())
	}
	model.clearInput()

	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 'n', Text: "N", Mod: tea.ModCtrl | tea.ModShift}))
	model = updated.(Model)
	if model.replyTarget != session.ID {
		t.Fatalf("configured Ctrl-Shift-N did not aim the composer: %q", model.replyTarget)
	}
	model.insertText("follow up")
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("Enter did not commit the reply")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if controller.sentSession != session.ID || controller.sentText != "follow up" {
		t.Fatalf("send = session %q text %q", controller.sentSession, controller.sentText)
	}

	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	model = updated.(Model)
	if model.backend != heikou.BackendCodex {
		t.Fatal("default Tab still cycled runner after rebinding")
	}
	model.replyTarget = ""
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyF6}))
	if updated.(Model).backend != heikou.BackendClaude {
		t.Fatal("configured F6 did not cycle runner")
	}
}

// The whole point of choosing the destination first is that the commit key is
// unambiguous: Enter starts a session or replies purely by composer state.
func TestEnterCommitsToWhicheverDestinationTheComposerShows(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	session := testDurableSession("018f0000-0000-4000-8000-000000000055", "", heikou.BackendCodex, "original", "/tmp", time.Now())
	model.snapshot.Sessions = []control.Session{session}
	model.setSnapshot(model.snapshot)
	model.selected = sessionRowKey(session)
	model.restoreSelection()

	model.insertText("start me")
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("Enter did not start a session from an unaimed composer")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if controller.startRequest.Prompt != "start me" || controller.sentText != "" {
		t.Fatalf("start = %q, send = %q", controller.startRequest.Prompt, controller.sentText)
	}
	// Starting moves the selection onto the brand-new session, which this fake
	// snapshot does not carry; re-select the live one before aiming at it.
	model.selected = sessionRowKey(session)
	model.restoreSelection()

	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	model = updated.(Model)
	if model.replyTarget != session.ID {
		t.Fatalf("Space did not aim the composer at the selection: %q", model.replyTarget)
	}
	if len(model.input) != 0 {
		t.Fatalf("the aiming Space leaked into the composer: %q", model.inputValue())
	}
	model.insertText("reply me")
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("Enter did not send from an aimed composer")
	}
	_ = cmd()
	if controller.sentSession != session.ID || controller.sentText != "reply me" {
		t.Fatalf("send = session %q text %q", controller.sentSession, controller.sentText)
	}
	if controller.startRequest.Prompt != "start me" {
		t.Fatalf("the reply also started a session: %q", controller.startRequest.Prompt)
	}
}

// A pinned target is what stops list navigation from silently redirecting a
// half-written message to whichever row the cursor drifted onto.
func TestReplyTargetIsPinnedAgainstLaterSelectionChanges(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	first := testDurableSession("018f0000-0000-4000-8000-000000000056", "", heikou.BackendCodex, "first", "/tmp", now)
	second := testDurableSession("018f0000-0000-4000-8000-000000000057", "", heikou.BackendCodex, "second", "/tmp", now)
	model.snapshot.Sessions = []control.Session{first, second}
	model.setSnapshot(model.snapshot)
	model.selected = sessionRowKey(first)
	model.restoreSelection()

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	model = updated.(Model)
	model.insertText("for the first one")
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	model = updated.(Model)
	if selected, ok := model.selectedSession(); !ok || selected.ID != second.ID {
		t.Fatal("Down did not move the selection while drafting")
	}
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("Enter did not send")
	}
	_ = cmd()
	if controller.sentSession != first.ID {
		t.Fatalf("send went to %q, want the pinned target %q", controller.sentSession, first.ID)
	}
}

func TestReplyModeSurvivesEscapeLadderAndShowsItsTarget(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 100, 24
	session := testDurableSession("018f0000-0000-4000-8000-000000000058", "", heikou.BackendClaude, "original", "/tmp", time.Now())
	model.snapshot.Sessions = []control.Session{session}
	model.setSnapshot(model.snapshot)
	model.selected = sessionRowKey(session)
	model.restoreSelection()

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	model = updated.(Model)
	model.insertText("drafting")
	if plain := ansi.Strip(model.View().Content); !strings.Contains(plain, "reply "+shortID(session.ID)) {
		t.Fatalf("composer does not name its reply target:\n%s", plain)
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(Model)
	if len(model.input) != 0 || model.replyTarget != session.ID {
		t.Fatalf("first Esc should clear text but keep the target: input %q target %q", model.inputValue(), model.replyTarget)
	}
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(Model)
	if model.replyTarget != "" || cmd != nil {
		t.Fatalf("second Esc should leave reply mode without quitting: target %q", model.replyTarget)
	}
	if plain := ansi.Strip(model.View().Content); !strings.Contains(plain, string(heikou.BackendCodex)+" · ") {
		t.Fatalf("composer did not return to the new-session prefix:\n%s", plain)
	}
}

// A dead target cannot receive the draft, so the prefix must stop promising it.
func TestReplyModeReleasesATargetThatStopped(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	session := testDurableSession("018f0000-0000-4000-8000-000000000059", "", heikou.BackendCodex, "original", "/tmp", time.Now())
	model.snapshot.Sessions = []control.Session{session}
	model.setSnapshot(model.snapshot)
	model.selected = sessionRowKey(session)
	model.restoreSelection()
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	model = updated.(Model)

	stopped := session
	stopped.Runtime = nil
	stopped.Status = control.StatusExited
	model.snapshotFetch.generation, model.snapshotFetch.activeGeneration = 1, 1
	updated, _ = model.Update(snapshotMsg{
		generation: 1,
		snapshot:   control.Snapshot{Sessions: []control.Session{stopped}, StatePath: "/tmp/heikou-test-state.json"},
	})
	model = updated.(Model)
	if model.replyTarget != "" {
		t.Fatalf("reply target survived its runtime: %q", model.replyTarget)
	}
	if !strings.Contains(model.notice, "reply target ended") {
		t.Fatalf("notice = %q; want it to explain the released target", model.notice)
	}
}

func TestReplyKeyRequiresALiveSelection(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	container := testWorkstream("018f0000-0000-4000-8000-000000000060", "Core", []string{"/tmp"}, time.Now())
	model.snapshot.Workstreams = []workstream.Workstream{container}
	model.setSnapshot(model.snapshot)
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	model = updated.(Model)
	if model.replyTarget != "" {
		t.Fatalf("a workstream row became a reply target: %q", model.replyTarget)
	}
	if !strings.Contains(model.errorText, "live session") {
		t.Fatalf("error = %q; want it to ask for a live session", model.errorText)
	}
	if len(model.input) != 0 {
		t.Fatalf("the refused Space leaked into the composer: %q", model.inputValue())
	}
}

// Enter is no longer overloaded, so cycling is safe mid-draft — which is the
// point at which you actually discover you want a different runner.
func TestCycleKeysWorkWhileTheComposerHasText(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	container := testWorkstream("018f0000-0000-4000-8000-000000000061", "Multi repo", []string{"/tmp/api", "/tmp/web"}, now)
	model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{container}})
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()
	model.insertText("half a thought")

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	model = updated.(Model)
	if model.backend != heikou.BackendClaude {
		t.Fatalf("Tab did not cycle the runner mid-draft: %q", model.backend)
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	model = updated.(Model)
	if got := model.launchRoot(); got != "/tmp/web" {
		t.Fatalf("Shift-Tab did not cycle the root mid-draft: %q", got)
	}
	if model.inputValue() != "half a thought" {
		t.Fatalf("cycling disturbed the draft: %q", model.inputValue())
	}
}

func TestSettingsRendersComposerBindings(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height, model.screen = 80, 24, screenSettings
	model.settings.ComposerKeys = config.ComposerKeys{Reply: "ctrl+n", CycleRunner: "f6", CycleRoot: "alt+r"}
	plain := ansi.Strip(model.View().Content)
	for _, want := range []string{"composer keys", "Enter", "Ctrl+N", "F6", "Alt+R"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("settings view is missing %q:\n%s", want, plain)
		}
	}
}

func TestSmallSettingsPaneScrollsToLaunchCommands(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height, model.screen = 40, 15, screenSettings
	if strings.Contains(ansi.Strip(model.View().Content), "launch commands") {
		t.Fatal("test fixture no longer needs scrolling")
	}
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
	model = updated.(Model)
	plain := ansi.Strip(model.View().Content)
	if !strings.Contains(plain, "launch commands") || !strings.Contains(plain, "codex") {
		t.Fatalf("settings did not scroll to launch commands:\n%s", plain)
	}
}

func TestCtrlXStopsRuntimeBeforeDeletingDurableRecord(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	session := testDurableSession("018f0000-0000-4000-8000-000000000060", "", heikou.BackendCodex, "cleanup", "/tmp", time.Now())
	model.snapshot.Sessions = []control.Session{session}
	model.setSnapshot(model.snapshot)
	model.selected = sessionRowKey(session)
	model.restoreSelection()

	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: 'x', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if cmd != nil || model.confirmStop != session.ID {
		t.Fatal("first Ctrl-X did not arm runtime stop")
	}
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: 'x', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("second Ctrl-X did not stop runtime")
	}
	_ = cmd()
	if controller.stopped != session.ID || controller.deleted != "" {
		t.Fatalf("cleanup called stop=%q delete=%q", controller.stopped, controller.deleted)
	}

	session.Runtime = nil
	session.Status = control.StatusStopped
	model.busy = false
	model.confirmStop = ""
	model.snapshot.Sessions = []control.Session{session}
	model.setSnapshot(model.snapshot)
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: 'x', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if cmd != nil || model.confirmDelete != session.ID {
		t.Fatal("first Ctrl-X did not arm durable record deletion")
	}
	_, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: 'x', Mod: tea.ModCtrl}))
	if cmd == nil {
		t.Fatal("second Ctrl-X did not delete durable record")
	}
	_ = cmd()
	if controller.deleted != session.ID {
		t.Fatalf("deleted session = %q", controller.deleted)
	}
}

func TestCtrlXAlsoStopsHighlightedOrganizerSession(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	session := testDurableSession("018f0000-0000-4000-8000-000000000061", "", heikou.BackendCodex, "cleanup", "/tmp", time.Now())
	model.snapshot.Sessions = []control.Session{session}
	model.setSnapshot(model.snapshot)
	model.selected = ungroupedKey
	model.restoreSelection()
	model.openOrganizer()
	model.selectOrganizerKey(sessionRowKey(session))

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 'x', Mod: tea.ModCtrl}))
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: 'x', Mod: tea.ModCtrl}))
	if cmd == nil {
		t.Fatal("organizer Ctrl-X did not stop highlighted runtime")
	}
	_ = updated
	_ = cmd()
	if controller.stopped != session.ID {
		t.Fatalf("stopped session = %q", controller.stopped)
	}
}

func TestSettingsErrorsSurviveSnapshotRefresh(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height, model.screen = 80, 24, screenSettings
	updated, _ := model.Update(settingsMsg{err: context.DeadlineExceeded})
	model = updated.(Model)
	model.snapshotFetch.generation = 1
	model.snapshotFetch.activeGeneration = 1
	updated, _ = model.Update(snapshotMsg{generation: 1, snapshot: control.Snapshot{}})
	model = updated.(Model)
	if !strings.Contains(model.errorText, "deadline exceeded") {
		t.Fatalf("settings error was cleared by refresh: %q", model.errorText)
	}
}

func TestPastePreservesMultilineComposer(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	updated, _ := model.Update(tea.PasteMsg{Content: "first line\r\nsecond\tline\nthird"})
	if got := updated.(Model).inputValue(); got != "first line\nsecond\tline\nthird" {
		t.Fatalf("input = %q", got)
	}
}

func TestShiftEnterCreatesMultilineLaunch(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	model.insertText("\tfirst line")
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}))
	model = updated.(Model)
	if cmd != nil || model.inputValue() != "\tfirst line\n" {
		t.Fatalf("Shift-Enter = input %q, command %v", model.inputValue(), cmd != nil)
	}
	model.insertText("second line")
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: 'j', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if cmd != nil || model.inputValue() != "\tfirst line\nsecond line\n" {
		t.Fatalf("Ctrl-J = input %q, command %v", model.inputValue(), cmd != nil)
	}
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd == nil {
		t.Fatal("Enter did not launch the multiline prompt")
	}
	_ = updated
	_ = cmd()
	if controller.startRequest.Prompt != "\tfirst line\nsecond line\n" {
		t.Fatalf("launch prompt = %q", controller.startRequest.Prompt)
	}
}

func TestMacComposerNavigation(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.insertText("alpha beta\ngamma delta")
	secondLine := len(splitGraphemes("alpha beta\n"))

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft, Mod: tea.ModSuper}))
	model = updated.(Model)
	if model.inputCursor != secondLine {
		t.Fatalf("Command-Left cursor = %d, want %d", model.inputCursor, secondLine)
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight, Mod: tea.ModAlt}))
	model = updated.(Model)
	if model.inputCursor != secondLine+len(splitGraphemes("gamma")) {
		t.Fatalf("Option-Right cursor = %d", model.inputCursor)
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 'b', Mod: tea.ModAlt}))
	model = updated.(Model)
	if model.inputCursor != secondLine {
		t.Fatalf("legacy Option-Left cursor = %d, want %d", model.inputCursor, secondLine)
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight, Mod: tea.ModMeta}))
	model = updated.(Model)
	if model.inputCursor != secondLine+len(splitGraphemes("gamma")) {
		t.Fatalf("Meta Option-Right cursor = %d", model.inputCursor)
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight, Mod: tea.ModSuper}))
	model = updated.(Model)
	if model.inputCursor != len(model.input) {
		t.Fatalf("Command-Right cursor = %d, want %d", model.inputCursor, len(model.input))
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp, Mod: tea.ModSuper}))
	model = updated.(Model)
	if model.inputCursor != 0 {
		t.Fatalf("Command-Up cursor = %d", model.inputCursor)
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown, Mod: tea.ModSuper}))
	if updated.(Model).inputCursor != len(model.input) {
		t.Fatalf("Command-Down cursor = %d, want %d", updated.(Model).inputCursor, len(model.input))
	}
}

func TestMacComposerDeletionUsesLogicalLines(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.insertText("alpha beta\ngamma delta")
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace, Mod: tea.ModSuper}))
	model = updated.(Model)
	if got := model.inputValue(); got != "alpha beta\n" {
		t.Fatalf("Command-Delete input = %q", got)
	}

	model.clearInput()
	model.insertText("alpha beta\ngamma delta")
	model.inputCursor = len(splitGraphemes("alpha beta\ngamma "))
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if got := model.inputValue(); got != "alpha beta\ndelta" {
		t.Fatalf("Ctrl-U input = %q", got)
	}

	model.clearInput()
	model.insertText("alpha beta")
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace, Mod: tea.ModAlt}))
	if got := updated.(Model).inputValue(); got != "alpha " {
		t.Fatalf("Option-Delete input = %q", got)
	}

	model.clearInput()
	model.insertText("first\nsecond")
	model.inputCursor = len(splitGraphemes("first"))
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 'k', Mod: tea.ModCtrl}))
	if got := updated.(Model).inputValue(); got != "firstsecond" {
		t.Fatalf("Ctrl-K at line end input = %q", got)
	}
}

func TestMultilineArrowsKeepPreferredColumnAndSelection(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.insertText("abcd\nx\nwxyz")
	model.inputCursor = len(splitGraphemes("abcd"))
	selected := model.selected
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	model = updated.(Model)
	if model.inputCursor != len(splitGraphemes("abcd\nx")) {
		t.Fatalf("first Down cursor = %d", model.inputCursor)
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	model = updated.(Model)
	if model.inputCursor != len(model.input) {
		t.Fatalf("second Down cursor = %d, want %d", model.inputCursor, len(model.input))
	}
	if model.selected != selected {
		t.Fatalf("multiline navigation changed dashboard selection from %q to %q", selected, model.selected)
	}
}

func TestMultilineComposerViewportFollowsCursor(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 60, 12
	model.insertText("line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7")
	plain := ansi.Strip(model.View().Content)
	if !strings.Contains(plain, "  7 │ line 7") || strings.Contains(plain, "  2 │ line 2") {
		t.Fatalf("composer viewport did not follow the cursor:\n%s", plain)
	}
	assertViewFits(t, model.View().Content, model.width, model.height)
}

func TestComposerKeepsSpacesAndGraphemeClusters(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.insertText("a 👩🏽‍💻 e\u0301")
	if got := model.inputValue(); got != "a 👩🏽‍💻 e\u0301" {
		t.Fatalf("input = %q", got)
	}
	if got, want := len(splitGraphemes("👩🏽‍💻e\u0301")), 2; got != want {
		t.Fatalf("grapheme count = %d, want %d", got, want)
	}
}

func TestUntrustedMetadataCannotInjectTerminalEscapes(t *testing.T) {
	model, _ := newTestModel("/tmp/\x1b]52;c;cm9vdA==\x07root", heikou.BackendCodex)
	model.width, model.height = 80, 24
	session := testDurableSession("018f0000-0000-4000-8000-000000000040", "", heikou.BackendCodex, "safe\x1b]52;c;c2VjcmV0\x07task", "/tmp/\x1b[31mred\x1b[0m", time.Now())
	model.snapshot.Sessions = []control.Session{session}
	model.setSnapshot(model.snapshot)
	model.selected = sessionRowKey(session)
	model.restoreSelection()
	model.previewID, model.preview = session.ID, "preview\x1b]52;c;c2VjcmV0\x07"
	content := model.View().Content
	if strings.Contains(content, "\x1b]52") || strings.Contains(content, "c2VjcmV0") {
		t.Fatalf("view retained an OSC payload: %q", content)
	}
}

func newTestModel(root string, backend heikou.Backend) (Model, *fakeController) {
	controller := &fakeController{snapshot: control.Snapshot{StatePath: "/tmp/heikou-test-state.json"}}
	return New(controller, root, backend, config.Store{Path: "/tmp/heikou-test-config.json"}, config.Default()), controller
}

func testWorkstream(id, name string, roots []string, now time.Time) workstream.Workstream {
	return workstream.Workstream{ID: id, Name: name, Roots: roots, ArtifactDir: "/tmp/artifacts/" + id, Revision: 1, CreatedAt: now, UpdatedAt: now}
}

func testDurableSession(id, workstreamID string, backend heikou.Backend, prompt, root string, now time.Time) control.Session {
	runtime := heikou.Session{ID: id, Name: "h-" + id, PaneID: "%1", Backend: backend, Prompt: prompt, Root: root, CurrentPath: root, Status: heikou.StatusLive, StartedAt: now.Add(-time.Minute), LastActivityAt: now}
	record := workstream.SessionRecord{ID: id, Backend: backend, InitialPrompt: prompt, InitialRoot: root, CreatedAt: now.Add(-time.Minute), Launch: workstream.LaunchIntent{Status: workstream.LaunchPending}}
	return control.Session{ID: id, Backend: backend, Prompt: prompt, Root: root, CreatedAt: record.CreatedAt, WorkstreamID: workstreamID, Status: control.StatusLive, Durable: true, Record: record, Runtime: &runtime}
}

func testOrphan(id, root string, now time.Time) control.Session {
	runtime := heikou.Session{ID: id, Name: "h-" + id, PaneID: "%2", Backend: heikou.BackendNoAgent, Prompt: "legacy pane", Root: root, CurrentPath: root, Status: heikou.StatusLive, StartedAt: now}
	return control.Session{ID: id, Backend: runtime.Backend, Prompt: runtime.Prompt, Root: root, CreatedAt: now, Status: control.StatusLive, Orphaned: true, Runtime: &runtime}
}

func findOrganizerRow(rows []listRow, key string) (listRow, bool) {
	for _, row := range rows {
		if row.key == key {
			return row, true
		}
	}
	return listRow{}, false
}

func assertViewFits(t *testing.T, content string, width, height int) {
	t.Helper()
	if !utf8.ValidString(content) {
		t.Fatalf("view contains invalid UTF-8 at %dx%d: %q", width, height, content)
	}
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		t.Fatalf("view has %d lines, terminal height %d\n%s", len(lines), height, ansi.Strip(content))
	}
	for index, line := range lines {
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("line %d width = %d, terminal width %d: %q", index, got, width, ansi.Strip(line))
		}
	}
}
