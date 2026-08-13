package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/zamborg/heikou/internal/config"
	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/control/controltest"
	"github.com/zamborg/heikou/internal/format"
	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/workstream"
)

// fakeController records the calls these tests assert on and inherits the rest
// from controltest.Stub, so a new method on control.Service breaks the stub
// rather than every fake in the repository.
type fakeController struct {
	controltest.Stub

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
	createdWorkstream      string
	renamedWorkstream      string
	renamedValue           string
}

func (f *fakeController) Snapshot(context.Context) (control.Snapshot, error) { return f.snapshot, nil }
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
func (f *fakeController) CreateWorkstream(_ context.Context, name, _ string, _ []string) (workstream.Workstream, error) {
	f.createdWorkstream = name
	return workstream.Workstream{}, nil
}
func (f *fakeController) RenameWorkstream(_ context.Context, id, name string) error {
	f.renamedWorkstream, f.renamedValue = id, name
	return nil
}
func (f *fakeController) SetSessionTitle(_ context.Context, id, title string) error {
	f.titledSession, f.titleValue = id, title
	return nil
}
func (f *fakeController) ReorderWorkstream(_ context.Context, id string, delta int) (bool, error) {
	f.reorderedWorkstream, f.reorderedDelta = id, delta
	return !f.reorderNoop, nil
}
func (f *fakeController) MoveSession(_ context.Context, sessionID, workstreamID string) error {
	f.movedSession, f.movedTarget = sessionID, workstreamID
	return nil
}

func (f *fakeController) AdoptSession(_ context.Context, sessionID, workstreamID string) (control.Session, error) {
	f.adoptSession, f.adoptTarget = sessionID, workstreamID
	return control.Session{}, nil
}
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
	row := model.renderSessionRow(session, false)
	plain := ansi.Strip(row)
	if width := ansi.StringWidth(row); width != model.width {
		t.Errorf("row width = %d, want %d: %q", width, model.width, plain)
	}
	if !strings.Contains(plain, "live") || !strings.Contains(plain, session.Record.Title) {
		t.Errorf("row lost status or title at 40 columns: %q", plain)
	}
	if strings.Contains(plain, "codex") || strings.Contains(plain, format.ShortID(session.ID)) {
		t.Errorf("row retained lower-priority metadata ahead of the title: %q", plain)
	}
}

func TestMediumSessionRowsRestoreRunnerWithoutCrowdingOutTitle(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width = sessionRowRunnerMinWidth
	session := testDurableSession("018f0000-0000-4000-8000-000000000003", "", heikou.BackendCodex, "initial task", "/tmp", time.Now())
	session.Record.Title = "Release Linux build"

	row := model.renderSessionRow(session, false)
	plain := ansi.Strip(row)
	if width := ansi.StringWidth(row); width != model.width {
		t.Errorf("row width = %d, want %d: %q", width, model.width, plain)
	}
	if !strings.Contains(plain, "codex") || !strings.Contains(plain, session.Record.Title) {
		t.Errorf("row did not restore runner alongside the title: %q", plain)
	}
	if strings.Contains(plain, format.ShortID(session.ID)) {
		t.Errorf("row restored the short ID before the rich-layout threshold: %q", plain)
	}
}

// TestWideRowsStayInsideThePane covers the rich layout, which the narrow and
// medium cases above never reach because both run below sessionRowRichMinWidth.
// The move mark widens the row prefix, so it is exercised in both states.
func TestWideRowsStayInsideThePane(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	session := testDurableSession("018f0000-0000-4000-8000-000000000005", "", heikou.BackendNoAgent, "initial task", "/tmp", time.Now())
	session.Record.Title = strings.Repeat("a very long title ", 12)

	for _, width := range []int{40, sessionRowRunnerMinWidth, sessionRowRichMinWidth, 80, 120} {
		model.width = width
		for _, selected := range []bool{false, true} {
			for _, marked := range []bool{false, true} {
				model.markedSession = ""
				if marked {
					model.markedSession = session.ID
				}
				row := model.renderSessionRow(session, selected)
				if got := ansi.StringWidth(row); got != width {
					t.Errorf("row width = %d, want %d (selected=%v, marked=%v): %q",
						got, width, selected, marked, ansi.Strip(row))
				}
			}
		}
	}
}

func TestMarkedSessionStaysIdentifiableAfterTheCursorLeaves(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width = 100
	session := testDurableSession("018f0000-0000-4000-8000-000000000006", "", heikou.BackendCodex, "marked work", "/tmp", time.Now())

	unmarked := ansi.Strip(model.renderSessionRow(session, false))
	model.markedSession = session.ID
	marked := ansi.Strip(model.renderSessionRow(session, false))

	if strings.Contains(unmarked, "◆") {
		t.Fatalf("unmarked row carries the move mark: %q", unmarked)
	}
	if !strings.Contains(marked, "◆") {
		t.Fatalf("marked row lost its move mark: %q", marked)
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

// Organize verbs are chords precisely so that every printable key stays the
// composer's. A bare n or r has to reach the draft untouched.
func TestOrganizeChordsDoNotStealPrintableInput(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	for _, key := range []tea.Key{
		{Code: 'n', Text: "n"},
		{Code: 'r', Text: "r"},
		{Code: 't', Text: "t"},
		{Code: 'm', Text: "m"},
		{Code: 'a', Text: "a"},
	} {
		updated, _ := model.Update(tea.KeyPressMsg(key))
		model = updated.(Model)
	}
	if model.inputValue() != "nrtma" {
		t.Fatalf("printable organize letters did not reach the composer: %q", model.inputValue())
	}
	if model.composerEdit != composerEditNone {
		t.Fatalf("a printable letter opened an organize edit: %v", model.composerEdit)
	}
}

func TestCtrlNNamesANewWorkstreamThroughTheComposer(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 120, 30

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 'n', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if model.composerEdit != composerEditCreateWorkstream {
		t.Fatalf("Ctrl-N did not begin workstream creation: %v", model.composerEdit)
	}
	if !strings.Contains(ansi.Strip(model.composerPrefix()), "new workstream") {
		t.Fatalf("composer prefix does not name the destination: %q", ansi.Strip(model.composerPrefix()))
	}

	// The draft is ordinary composer text, so the reply key stays typeable.
	for _, key := range []tea.Key{
		{Code: 'A', Text: "A", Mod: tea.ModShift},
		{Code: tea.KeySpace, Text: " "},
		{Code: 'b', Text: "b"},
	} {
		updated, _ = model.Update(tea.KeyPressMsg(key))
		model = updated.(Model)
	}
	if model.inputValue() != "A b" {
		t.Fatalf("workstream name draft = %q, want %q", model.inputValue(), "A b")
	}
	if model.replyTarget != "" {
		t.Fatal("the reply key switched destinations while naming a workstream")
	}

	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("Enter did not commit the new workstream")
	}
	_ = cmd()
	if controller.createdWorkstream != "A b" {
		t.Fatalf("created workstream = %q, want %q", controller.createdWorkstream, "A b")
	}
}

// F3 no longer opens a view; it is the one catch-all re-read. Notes and files
// are cached until the selection moves, so this is the only way to pick up an
// external write under a stationary cursor.
func TestF3ForcesARefreshWithoutChangingScreen(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyF3}))
	model = updated.(Model)
	if model.screen != screenDashboard {
		t.Fatalf("F3 left the dashboard: screen=%v", model.screen)
	}
	if cmd == nil {
		t.Fatal("F3 did not request a refresh")
	}
	if model.notice != "refreshed" {
		t.Fatalf("F3 notice = %q, want %q", model.notice, "refreshed")
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
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyF1}))
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(Model)
	if model.screen != screenDashboard || model.overlay != overlayNone {
		t.Fatalf("closing dashboard help = screen %v overlay %v", model.screen, model.overlay)
	}
}

// Ctrl-R carries the verb and the cursor supplies the noun: the same chord
// renames a workstream or retitles a session depending on the selected row.
func TestCtrlRRenamesWhicheverNounIsSelected(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	container := testWorkstream("018f0000-0000-4000-8000-000000000020", "Core", []string{"/tmp"}, now)
	session := testDurableSession("018f0000-0000-4000-8000-000000000021", container.ID, heikou.BackendCodex, "initial task", "/tmp", now)
	session.Record.Title = "Old title"
	model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{container}, Sessions: []control.Session{session}})
	model.selected = sessionRowKey(session)
	model.restoreSelection()

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if model.composerEdit != composerEditSessionTitle || model.inputValue() != "Old title" {
		t.Fatalf("session title edit = mode %v value %q", model.composerEdit, model.inputValue())
	}
	model.input = splitGraphemes("Release Linux build")
	model.inputCursor = len(model.input)
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
	if model.composerEdit != composerEditNone || model.inputValue() != "" {
		t.Fatalf("committing a title left the composer in edit mode: %v %q", model.composerEdit, model.inputValue())
	}

	// An empty draft is a real outcome here: it clears the durable title.
	session.Record.Title = "Release Linux build"
	model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{container}, Sessions: []control.Session{session}})
	model.selected = sessionRowKey(session)
	model.restoreSelection()
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	model = updated.(Model)
	model.clearInput()
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("clearing a session title did not produce a command")
	}
	_ = cmd()
	if controller.titledSession != session.ID || controller.titleValue != "" {
		t.Fatalf("clear title command = session %q title %q", controller.titledSession, controller.titleValue)
	}

	model.busy = false
	model.cancelComposerEdit()
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if model.composerEdit != composerEditRenameWorkstream || model.inputValue() != "Core" {
		t.Fatalf("workstream rename = mode %v value %q", model.composerEdit, model.inputValue())
	}
	model.input = splitGraphemes("Platform")
	model.inputCursor = len(model.input)
	_, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd == nil {
		t.Fatal("saving a workstream name did not produce a command")
	}
	_ = cmd()
	if controller.renamedWorkstream != container.ID || controller.renamedValue != "Platform" {
		t.Fatalf("rename command = workstream %q name %q", controller.renamedWorkstream, controller.renamedValue)
	}
}

// A rename is a destination, so Esc cancels it in one press rather than
// clearing the draft first and leaving the composer pointed somewhere odd.
func TestEscapeCancelsAnOrganizeEditInOnePress(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	container := testWorkstream("018f0000-0000-4000-8000-000000000022", "Core", []string{"/tmp"}, now)
	model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{container}})
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(Model)
	if model.composerEdit != composerEditNone || model.inputValue() != "" || model.composerEditTarget != "" {
		t.Fatalf("Esc did not cancel the edit: mode %v value %q target %q",
			model.composerEdit, model.inputValue(), model.composerEditTarget)
	}
}

func TestShiftArrowsReorderOnlyNamedWorkstreams(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	first := testWorkstream("018f0000-0000-4000-8000-000000000061", "First", []string{"/tmp"}, now)
	second := testWorkstream("018f0000-0000-4000-8000-000000000062", "Second", []string{"/tmp"}, now)
	model.snapshot.Workstreams = []workstream.Workstream{first, second}
	model.setSnapshot(model.snapshot)
	model.selected = workstreamRowKey(second.ID)
	model.restoreSelection()

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
	if model.selected != workstreamRowKey(second.ID) {
		t.Fatalf("reorder lost selected workstream: %q", model.selected)
	}

	model.busy = false
	model.selected = workstreamRowKey(first.ID)
	model.restoreSelection()
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
	model.selected = ungroupedKey
	model.restoreSelection()
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown, Mod: tea.ModShift}))
	model = updated.(Model)
	if cmd != nil || !strings.Contains(model.errorText, "Ungrouped") {
		t.Fatalf("synthetic reorder = command %v, error %q", cmd != nil, model.errorText)
	}
}

// On a session row the same chord walks the durable workstream order with
// Ungrouped pinned last, because sessions have no order inside a workstream to
// change. See todos/session-ordering.md.
func TestShiftArrowsMoveASessionBetweenAdjacentWorkstreams(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	first := testWorkstream("018f0000-0000-4000-8000-000000000064", "First", []string{"/tmp"}, now)
	second := testWorkstream("018f0000-0000-4000-8000-000000000065", "Second", []string{"/tmp"}, now)
	session := testDurableSession("018f0000-0000-4000-8000-000000000066", first.ID, heikou.BackendCodex, "task", "/tmp", now)
	model.setSnapshot(control.Snapshot{
		Workstreams: []workstream.Workstream{first, second},
		Sessions:    []control.Session{session},
	})
	model.selected = sessionRowKey(session)
	model.restoreSelection()

	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown, Mod: tea.ModShift}))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("Shift-Down on a session did not move it")
	}
	_ = cmd()
	if controller.movedSession != session.ID || controller.movedTarget != second.ID {
		t.Fatalf("move = (%q, %q), want (%q, %q)", controller.movedSession, controller.movedTarget, session.ID, second.ID)
	}

	// One past the last named workstream is Ungrouped, which is a real
	// destination rather than a wall.
	model.busy = false
	session.WorkstreamID = second.ID
	model.setSnapshot(control.Snapshot{
		Workstreams: []workstream.Workstream{first, second},
		Sessions:    []control.Session{session},
	})
	model.selected = sessionRowKey(session)
	model.restoreSelection()
	_, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown, Mod: tea.ModShift}))
	if cmd == nil {
		t.Fatal("Shift-Down past the last workstream did not reach Ungrouped")
	}
	_ = cmd()
	if controller.movedTarget != "" {
		t.Fatalf("move target = %q, want Ungrouped", controller.movedTarget)
	}

	// And Ungrouped is the end of the walk.
	model.busy = false
	session.WorkstreamID = ""
	model.setSnapshot(control.Snapshot{
		Workstreams: []workstream.Workstream{first, second},
		Sessions:    []control.Session{session},
	})
	model.selected = sessionRowKey(session)
	model.restoreSelection()
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown, Mod: tea.ModShift}))
	model = updated.(Model)
	if cmd != nil || !strings.Contains(model.errorText, "last workstream") {
		t.Fatalf("past-the-end move = command %v, error %q", cmd != nil, model.errorText)
	}
}

func TestResizeModeAdjustsTheDashboardSplit(t *testing.T) {
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

// Ctrl-T carries both halves of a move: it marks on a session row and completes
// on a workstream row, so the cursor is free to travel in between.
func TestCtrlTMarksASessionAndMovesItToTheSelectedWorkstream(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	core := testWorkstream("018f0000-0000-4000-8000-000000000038", "Core", []string{"/tmp/core"}, now)
	web := testWorkstream("018f0000-0000-4000-8000-000000000039", "Web", []string{"/tmp/web"}, now)
	member := testDurableSession("018f0000-0000-4000-8000-00000000003a", core.ID, heikou.BackendCodex, "member", "/tmp/core", now)
	model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{core, web}, Sessions: []control.Session{member}})
	model.selected = sessionRowKey(member)
	model.restoreSelection()

	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: 't', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if cmd != nil || model.markedSession != member.ID {
		t.Fatalf("Ctrl-T on a session did not mark it: marked=%q cmd=%v", model.markedSession, cmd != nil)
	}

	model.selected = workstreamRowKey(web.ID)
	model.restoreSelection()
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: 't', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("Ctrl-T on the destination did not produce a move command")
	}
	message := cmd()
	if controller.movedSession != member.ID || controller.movedTarget != web.ID {
		t.Fatalf("move = session %q target %q", controller.movedSession, controller.movedTarget)
	}
	updated, _ = model.Update(message)
	model = updated.(Model)
	if model.markedSession != "" {
		t.Fatalf("a completed move left the mark behind: %q", model.markedSession)
	}
}

func TestCtrlTOnTheSameSessionCancelsTheMark(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	session := testDurableSession("018f0000-0000-4000-8000-00000000003e", "", heikou.BackendCodex, "task", "/tmp", now)
	model.setSnapshot(control.Snapshot{Sessions: []control.Session{session}})
	model.selected = sessionRowKey(session)
	model.restoreSelection()

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 't', Mod: tea.ModCtrl}))
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 't', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if model.markedSession != "" {
		t.Fatalf("a second Ctrl-T did not cancel the mark: %q", model.markedSession)
	}

	// Esc releases it too, before it falls through to the Ungrouped reset.
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 't', Mod: tea.ModCtrl}))
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(Model)
	if model.markedSession != "" {
		t.Fatalf("Esc did not cancel the mark: %q", model.markedSession)
	}
}

// Adoption stays explicit: an orphan has no durable record to move, so the
// shared chord issues a different command and refuses the synthetic inbox.
func TestCtrlTAdoptsAnOrphanIntoANamedWorkstreamOnly(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	container := testWorkstream("018f0000-0000-4000-8000-00000000003f", "Core", []string{"/tmp"}, now)
	orphan := testOrphan("018f0000-0000-4000-8000-00000000003b", "/tmp", now)
	model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{container}, Orphans: []control.Session{orphan}})
	model.selected = sessionRowKey(orphan)
	model.restoreSelection()

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 't', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if model.markedSession != orphan.ID {
		t.Fatalf("orphan was not marked: %q", model.markedSession)
	}

	model.selected = ungroupedKey
	model.restoreSelection()
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: 't', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if cmd != nil || !strings.Contains(model.errorText, "named workstream") {
		t.Fatalf("adopting into Ungrouped = command %v, error %q", cmd != nil, model.errorText)
	}

	model.busy = false
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()
	_, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: 't', Mod: tea.ModCtrl}))
	if cmd == nil {
		t.Fatal("Ctrl-T on a named workstream did not adopt the orphan")
	}
	_ = cmd()
	if controller.adoptSession != orphan.ID || controller.adoptTarget != container.ID {
		t.Fatalf("adoption = session %q target %q", controller.adoptSession, controller.adoptTarget)
	}
}

func TestCreatedWorkstreamBecomesTheDashboardSelection(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendClaude)
	now := time.Now()
	destination := testWorkstream("018f0000-0000-4000-8000-000000000074", "First project", []string{"/tmp"}, now)

	updated, _ := model.Update(workstreamMsg{action: "create", item: destination})
	model = updated.(Model)
	model.snapshot.Workstreams = []workstream.Workstream{destination}
	model.setSnapshot(model.snapshot)
	model.restoreSelection()
	if model.selected != workstreamRowKey(destination.ID) {
		t.Fatalf("post-create selection = %q, want the new workstream", model.selected)
	}
	if model.launchWorkstreamID() != destination.ID {
		t.Fatalf("composer launch target = %q, want the new workstream", model.launchWorkstreamID())
	}
}

func TestSettingsAndHelpViewsStayWithinTerminal(t *testing.T) {
	for _, size := range []struct{ width, height int }{{1, 12}, {2, 12}, {8, 12}, {20, 12}, {40, 15}, {80, 24}, {120, 40}} {
		model, _ := newTestModel("/tmp", heikou.BackendCodex)
		model.width, model.height = size.width, size.height
		model.screen = screenSettings
		assertViewFits(t, model.View().Content, size.width, size.height)
		model.screen, model.overlay = screenDashboard, overlayHelp
		assertViewFits(t, model.View().Content, size.width, size.height)
	}
}

func TestSelectedRowsRemainValidAtNarrowWidths(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	container := testWorkstream("018f0000-0000-4000-8000-000000000053", "Narrow", []string{"/tmp"}, now)
	session := testDurableSession("018f0000-0000-4000-8000-000000000054", container.ID, heikou.BackendCodex, "narrow row", "/tmp", now)
	model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{container}, Sessions: []control.Session{session}})
	model.height = 12

	for width := 1; width <= 20; width++ {
		model.width = width
		for _, key := range []string{workstreamRowKey(container.ID), sessionRowKey(session)} {
			model.selected = key
			model.restoreSelection()
			assertViewFits(t, model.View().Content, width, model.height)
		}
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
	model.screen = screenSettings
	assertMode("SETTINGS", model.View().Content)
	model.screen, model.overlay = screenDashboard, overlayHelp
	assertMode("HELP", model.View().Content)
}

func TestDetailPaneShowsWorkstreamNotesAndArtifactTree(t *testing.T) {
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

	cmd := model.artifactContextCmd(true)
	if cmd == nil {
		t.Fatal("selecting a named workstream did not request artifact context")
	}
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	plain := ansi.Strip(model.View().Content)
	for _, want := range []string{"Payments", "NOTES", "Ship the smallest useful version.", "FILES", "docs/", "plan.md"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("dashboard detail pane is missing %q:\n%s", want, plain)
		}
	}

	// A session resolves to its parent workstream, so moving between a group and
	// its members must not cost another filesystem read.
	model.selected = sessionRowKey(session)
	model.restoreSelection()
	if cmd := model.artifactContextCmd(false); cmd != nil {
		t.Fatal("moving to a session in the same workstream re-read artifact context")
	}
	assertViewFits(t, model.View().Content, model.width, model.height)

	// A session row hands the pane back to its terminal preview.
	model.previewID, model.preview = session.ID, "terminal tail"
	if detail := ansi.Strip(model.renderDetails()); strings.Contains(detail, "NOTES") {
		t.Fatalf("a selected session still rendered workstream notes: %q", detail)
	}
}

// The artifact cache is keyed on the selection, so an external write under a
// stationary cursor is invisible until F3 forces the re-read. That is the whole
// reason F3 survives the organizer it used to open.
func TestF3RefreshPicksUpAnExternalNotesWrite(t *testing.T) {
	artifactDir := t.TempDir()
	notesPath := filepath.Join(artifactDir, "notes.md")
	if err := os.WriteFile(notesPath, []byte("before agent edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 100, 30
	container := testWorkstream("018f0000-0000-4000-8000-000000000022", "Refresh", []string{"/tmp"}, time.Now())
	container.ArtifactDir = artifactDir
	controller.snapshot = control.Snapshot{Workstreams: []workstream.Workstream{container}}
	model.setSnapshot(controller.snapshot)
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()
	initial := model.artifactContextCmd(true)
	if initial == nil {
		t.Fatal("initial context read was not requested")
	}
	updated, _ := model.Update(initial())
	model = updated.(Model)
	if model.artifactContext.snapshot.Notes != "before agent edit" {
		t.Fatalf("initial notes = %q", model.artifactContext.snapshot.Notes)
	}
	if err := os.WriteFile(notesPath, []byte("after agent edit"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Without F3 the cached read stands, because the selection never moved.
	if cmd := model.artifactContextCmd(false); cmd != nil {
		t.Fatal("a stationary cursor re-read the artifact context on its own")
	}

	updated, refresh := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyF3}))
	model = updated.(Model)
	if refresh == nil {
		t.Fatal("F3 did not force an artifact context refresh")
	}
	for _, message := range collectMessages(refresh) {
		updated, _ = model.Update(message)
		model = updated.(Model)
	}
	if model.artifactContext.snapshot.Notes != "after agent edit" {
		t.Fatalf("refreshed notes = %q", model.artifactContext.snapshot.Notes)
	}
}

func TestLargeContextPaneGivesNotesAndTreeMoreRoom(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 100, 40
	container := testWorkstream("018f0000-0000-4000-8000-000000000064", "Deep context", []string{"/tmp"}, time.Now())
	model.snapshot.Workstreams = []workstream.Workstream{container}
	model.setSnapshot(model.snapshot)
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()

	tree := make([]artifactTreeEntry, 12)
	for index := range tree {
		tree[index] = artifactTreeEntry{Name: "artifact-" + string(rune('a'+index)), Depth: 1, Regular: true}
	}
	model.artifactContext.snapshot = artifactContextSnapshot{
		WorkstreamID: container.ID,
		ArtifactDir:  container.ArtifactDir,
		Notes:        strings.Repeat("one useful note line\n", 12),
		NotesStatus:  artifactNotesReady,
		Tree:         tree,
	}
	// Ctrl-G grows the pane; the split inside it should follow.
	for range 100 {
		model.resizeLowerPane(1)
	}
	lines := model.renderWorkstreamArtifacts(container, model.detailHeight())
	filesIndex := -1
	for index, line := range lines {
		if strings.Contains(ansi.Strip(line), "FILES") {
			filesIndex = index
			break
		}
	}
	if filesIndex <= 4 {
		t.Fatalf("notes received only %d rows, want more than a three-row cap", filesIndex)
	}
	if treeRows := len(lines) - filesIndex - 1; treeRows <= 4 {
		t.Fatalf("artifact tree received only %d rows", treeRows)
	}
}

func TestArtifactContextIgnoresStaleSelectionRead(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	first := testWorkstream("018f0000-0000-4000-8000-000000000058", "First", []string{"/tmp"}, time.Now())
	first.ArtifactDir = t.TempDir()
	second := testWorkstream("018f0000-0000-4000-8000-000000000059", "Second", []string{"/tmp"}, time.Now())
	second.ArtifactDir = t.TempDir()
	model.snapshot.Workstreams = []workstream.Workstream{first, second}
	model.setSnapshot(model.snapshot)
	model.selected = workstreamRowKey(first.ID)
	model.restoreSelection()

	firstCmd := model.artifactContextCmd(true)
	model.selectRowKey(workstreamRowKey(second.ID))
	secondCmd := model.artifactContextCmd(false)
	if firstCmd == nil || secondCmd == nil {
		t.Fatal("selection changes did not produce distinct artifact reads")
	}
	updated, _ := model.Update(firstCmd())
	model = updated.(Model)
	if model.artifactContext.snapshot.WorkstreamID == first.ID {
		t.Fatal("stale first-workstream context replaced the current selection")
	}
	updated, _ = model.Update(secondCmd())
	model = updated.(Model)
	if model.artifactContext.snapshot.WorkstreamID != second.ID {
		t.Fatalf("current context = %q, want %q", model.artifactContext.snapshot.WorkstreamID, second.ID)
	}
}

func TestArtifactContextRetriesSelectionWhoseStaleReadCompletedElsewhere(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	first := testWorkstream("018f0000-0000-4000-8000-00000000005a", "First", []string{"/tmp"}, time.Now())
	first.ArtifactDir = t.TempDir()
	second := testWorkstream("018f0000-0000-4000-8000-00000000005b", "Second", []string{"/tmp"}, time.Now())
	second.ArtifactDir = t.TempDir()
	model.snapshot.Workstreams = []workstream.Workstream{first, second}
	model.setSnapshot(model.snapshot)
	model.selected = workstreamRowKey(first.ID)
	model.restoreSelection()

	firstCmd := model.artifactContextCmd(true)
	updated, _ := model.Update(firstCmd())
	model = updated.(Model)
	if model.artifactContext.snapshot.WorkstreamID != first.ID {
		t.Fatalf("cached context = %q, want %q", model.artifactContext.snapshot.WorkstreamID, first.ID)
	}

	model.selectRowKey(workstreamRowKey(second.ID))
	staleSecondCmd := model.artifactContextCmd(false)
	if staleSecondCmd == nil {
		t.Fatal("selecting the second workstream did not begin a context read")
	}
	model.selectRowKey(workstreamRowKey(first.ID))
	if cmd := model.artifactContextCmd(false); cmd != nil {
		t.Fatal("returning to cached first context unexpectedly re-read it")
	}
	if model.artifactContext.loadingKey != "" {
		t.Fatalf("abandoned second read remains marked loading: %q", model.artifactContext.loadingKey)
	}

	updated, _ = model.Update(staleSecondCmd())
	model = updated.(Model)
	model.selectRowKey(workstreamRowKey(second.ID))
	retryCmd := model.artifactContextCmd(false)
	if retryCmd == nil {
		t.Fatal("stale second completion permanently suppressed its retry")
	}
	updated, _ = model.Update(retryCmd())
	model = updated.(Model)
	if model.artifactContext.snapshot.WorkstreamID != second.ID {
		t.Fatalf("retried context = %q, want %q", model.artifactContext.snapshot.WorkstreamID, second.ID)
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

func TestListViewportKeepsSelectedSessionVisible(t *testing.T) {
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
	last := model.snapshot.Sessions[len(model.snapshot.Sessions)-1]
	model.selectRowKey(sessionRowKey(last))
	content := ansi.Strip(model.renderWorkstreams())
	if !strings.Contains(content, "task "+last.ID) {
		t.Fatalf("selected session is outside the list viewport:\n%s", content)
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

// The selection is locked while a reply is pinned, so the detail pane keeps
// showing the conversation being answered. The pin is still what guarantees
// delivery; the lock is what stops the screen from disagreeing with it.
func TestReplyModeLocksTheSelection(t *testing.T) {
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

	for _, key := range []tea.Key{{Code: tea.KeyDown}, {Code: tea.KeyUp}, {Code: tea.KeyPgDown}, {Code: tea.KeyPgUp}} {
		updated, _ = model.Update(tea.KeyPressMsg(key))
		model = updated.(Model)
		if selected, ok := model.selectedSession(); !ok || selected.ID != first.ID {
			t.Fatalf("%v moved the selection away from the reply target", key.Code)
		}
	}
	if !strings.Contains(model.notice, format.ShortID(first.ID)) {
		t.Fatalf("locked navigation did not say why: %q", model.notice)
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

// Navigation unlocks the moment the reply is released, and a multiline draft
// still gets its own vertical motion while the list is held.
func TestSelectionUnlocksAfterLeavingReplyMode(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	first := testDurableSession("018f0000-0000-4000-8000-00000000005c", "", heikou.BackendCodex, "first", "/tmp", now)
	second := testDurableSession("018f0000-0000-4000-8000-00000000005d", "", heikou.BackendCodex, "second", "/tmp", now)
	model.snapshot.Sessions = []control.Session{first, second}
	model.setSnapshot(model.snapshot)
	model.selected = sessionRowKey(first)
	model.restoreSelection()

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	model = updated.(Model)
	model.insertText("line one\nline two")
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	model = updated.(Model)
	if selected, ok := model.selectedSession(); !ok || selected.ID != first.ID {
		t.Fatal("multiline Up moved the selection instead of the cursor")
	}

	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	model = updated.(Model)
	if selected, ok := model.selectedSession(); !ok || selected.ID != second.ID {
		t.Fatal("leaving reply mode did not restore list navigation")
	}
}

// The pin covers one message, not the rest of the conversation: once the reply
// lands, the composer owes the next Enter a fresh, visible destination.
func TestADeliveredReplyReleasesItsTarget(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 100, 24
	session := testDurableSession("018f0000-0000-4000-8000-000000000061", "", heikou.BackendCodex, "original", "/tmp", time.Now())
	model.snapshot.Sessions = []control.Session{session}
	model.setSnapshot(model.snapshot)
	model.selected = sessionRowKey(session)
	model.restoreSelection()

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	model = updated.(Model)
	model.insertText("reply me")
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("Enter did not send from an aimed composer")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if controller.sentSession != session.ID || controller.sentText != "reply me" {
		t.Fatalf("send = session %q text %q", controller.sentSession, controller.sentText)
	}
	if model.replyTarget != "" {
		t.Fatalf("the composer stayed aimed after sending: %q", model.replyTarget)
	}
	if len(model.input) != 0 {
		t.Fatalf("the sent draft survived: %q", model.inputValue())
	}
	plain := ansi.Strip(model.View().Content)
	if strings.Contains(plain, "↳ reply ") {
		t.Fatalf("composer kept the reply prefix after sending:\n%s", plain)
	}
	if !strings.Contains(plain, string(heikou.BackendCodex)+" · ") {
		t.Fatalf("composer did not return to the new-session prefix:\n%s", plain)
	}
}

// A refused send leaves the message undelivered, so the destination and the
// text both have to survive for the retry the user is about to make.
func TestAFailedReplyKeepsItsTargetAndDraft(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 100, 24
	session := testDurableSession("018f0000-0000-4000-8000-000000000062", "", heikou.BackendCodex, "original", "/tmp", time.Now())
	model.snapshot.Sessions = []control.Session{session}
	model.setSnapshot(model.snapshot)
	model.selected = sessionRowKey(session)
	model.restoreSelection()

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}))
	model = updated.(Model)
	model.insertText("reply me")
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(Model)
	updated, _ = model.Update(sendMsg{id: session.ID, err: errors.New("tmux refused the keys")})
	model = updated.(Model)
	if model.replyTarget != session.ID {
		t.Fatalf("a failed send released the target: %q", model.replyTarget)
	}
	if model.inputValue() != "reply me" {
		t.Fatalf("a failed send cleared the draft: %q", model.inputValue())
	}
}

// One Esc leaves reply mode, and it takes the draft with it. A follow-up left
// behind in a composer now aimed at a new session would let the next Enter
// spawn a real one, which is the expensive direction to get wrong.
func TestOneEscapeLeavesReplyModeAndTakesTheDraft(t *testing.T) {
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
	if plain := ansi.Strip(model.View().Content); !strings.Contains(plain, "reply "+format.ShortID(session.ID)) {
		t.Fatalf("composer does not name its reply target:\n%s", plain)
	}

	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(Model)
	if model.replyTarget != "" || len(model.input) != 0 || cmd != nil {
		t.Fatalf("one Esc should release the reply and its draft: input %q target %q",
			model.inputValue(), model.replyTarget)
	}
	if plain := ansi.Strip(model.View().Content); !strings.Contains(plain, string(heikou.BackendCodex)+" · ") {
		t.Fatalf("composer did not return to the new-session prefix:\n%s", plain)
	}
}

// Esc never quits. With an empty composer it parks the cursor on Ungrouped, so
// a mashed Esc is a reset rather than an exit.
func TestEscapeResetsToUngroupedInsteadOfQuitting(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 100, 24
	now := time.Now()
	container := testWorkstream("018f0000-0000-4000-8000-00000000005e", "Core", []string{"/tmp"}, now)
	model.setSnapshot(control.Snapshot{Workstreams: []workstream.Workstream{container}})
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()

	for range 3 {
		updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
		model = updated.(Model)
		if cmd != nil {
			if message := cmd(); message != nil {
				if _, quit := message.(tea.QuitMsg); quit {
					t.Fatal("Esc quit the dashboard")
				}
			}
		}
		if model.selected != ungroupedKey {
			t.Fatalf("Esc did not park on Ungrouped: %q", model.selected)
		}
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

// collectMessages flattens a possibly batched command into the messages it
// produces, so a test can feed each one back through Update.
func collectMessages(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	message := cmd()
	batch, ok := message.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{message}
	}
	var messages []tea.Msg
	for _, child := range batch {
		messages = append(messages, collectMessages(child)...)
	}
	return messages
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
