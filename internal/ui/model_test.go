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
func (f *fakeController) ArchiveWorkstream(context.Context, string) error        { return nil }
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
		model.snapshot = control.Snapshot{Workstreams: []workstream.Workstream{container}, Sessions: []control.Session{session}, StatePath: "/tmp/heikou-state.json"}
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

func TestSessionViewsPreferLatestMessageSentThroughHeikou(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height = 100, 30
	session := testDurableSession("018f0000-0000-4000-8000-000000000005", "", heikou.BackendCodex, "initial task", "/tmp", time.Now())
	session.LastUserMessage = "most recent follow-up"
	model.snapshot.Sessions = []control.Session{session}
	model.selected = sessionRowKey(session)
	model.restoreSelection()
	model.previewID = session.ID

	row := ansi.Strip(model.renderSessionRow(session, false))
	if !strings.Contains(row, "most recent follow-up") || strings.Contains(row, "initial task") {
		t.Fatalf("dashboard row did not prefer latest message: %q", row)
	}
	details := ansi.Strip(model.renderDetails())
	if !strings.Contains(details, "you") || !strings.Contains(details, "most recent follow-up") {
		t.Fatalf("details did not label latest user message: %q", details)
	}
	organizer := ansi.Strip(model.renderOrganizerSessionRow(session, false, false))
	if !strings.Contains(organizer, "most recent follow-up") {
		t.Fatalf("organizer row did not prefer latest message: %q", organizer)
	}
}

func TestRowsGroupDurableSessionsAndKeepOrphansSeparate(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	container := testWorkstream("018f0000-0000-4000-8000-000000000010", "Core", []string{"/tmp"}, now)
	member := testDurableSession("018f0000-0000-4000-8000-000000000011", container.ID, heikou.BackendCodex, "member", "/tmp", now)
	ungrouped := testDurableSession("018f0000-0000-4000-8000-000000000012", "", heikou.BackendClaude, "inbox", "/tmp", now)
	orphan := testOrphan("018f0000-0000-4000-8000-000000000013", "/tmp", now)
	model.snapshot = control.Snapshot{Workstreams: []workstream.Workstream{container}, Sessions: []control.Session{member, ungrouped}, Orphans: []control.Session{orphan}}
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
	model.snapshot = control.Snapshot{Workstreams: []workstream.Workstream{container}}
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
	if model.settingsOpen || model.inputValue() != "s" {
		t.Fatalf("printable s changed mode/input: open=%v input=%q", model.settingsOpen, model.inputValue())
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 's', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if !model.settingsOpen {
		t.Fatal("Ctrl-S did not open settings")
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if updated.(Model).settingsOpen {
		t.Fatal("Escape did not return to dashboard")
	}
}

func TestF3OpensOrganizerWithoutStealingPrintableInput(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyF3}))
	model = updated.(Model)
	if !model.organizerOpen {
		t.Fatal("F3 did not open workstreams")
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 'n', Text: "n"}))
	model = updated.(Model)
	if model.organizerEdit != "create" {
		t.Fatal("n did not begin workstream creation")
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: 'N', Text: "N", Mod: tea.ModShift}))
	if updated.(Model).organizerValue() != "N" {
		t.Fatal("organizer editor did not accept text")
	}
}

func TestOrganizerShowsExpandableSessionTree(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	container := testWorkstream("018f0000-0000-4000-8000-000000000034", "Core", []string{"/tmp/core"}, now)
	member := testDurableSession("018f0000-0000-4000-8000-000000000035", container.ID, heikou.BackendCodex, "member task", "/tmp/core", now)
	ungrouped := testDurableSession("018f0000-0000-4000-8000-000000000036", "", heikou.BackendClaude, "inbox task", "/tmp", now)
	orphan := testOrphan("018f0000-0000-4000-8000-000000000037", "/tmp", now)
	model.snapshot = control.Snapshot{Workstreams: []workstream.Workstream{container}, Sessions: []control.Session{member, ungrouped}, Orphans: []control.Session{orphan}}
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

func TestOrganizerEditsAndRemovesSelectedRoot(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	container := testWorkstream("018f0000-0000-4000-8000-000000000033", "Core", []string{"/tmp/api", "/tmp/web"}, time.Now())
	model.snapshot.Workstreams = []workstream.Workstream{container}
	model.selected = workstreamRowKey(container.ID)
	model.rootIndex[container.ID] = 1
	model.openOrganizer()

	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: 'P', Text: "P", Mod: tea.ModShift}))
	model = updated.(Model)
	if cmd != nil || model.organizerEdit != "root_replace" || model.organizerRootTarget != "/tmp/web" {
		t.Fatalf("Shift-P did not edit selected root: mode=%q target=%q", model.organizerEdit, model.organizerRootTarget)
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
	model.selected = workstreamRowKey(container.ID)
	model.openOrganizer()

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	model = updated.(Model)
	if model.confirmRootRemoval == "" {
		t.Fatal("first d did not arm root removal")
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyF1}))
	model = updated.(Model)
	if !model.helpOpen || model.confirmRootRemoval != "" {
		t.Fatalf("help did not clear root confirmation: help=%v confirmation=%q", model.helpOpen, model.confirmRootRemoval)
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
	model.snapshot = control.Snapshot{Workstreams: []workstream.Workstream{core, web}, Sessions: []control.Session{member}}
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
	if !model.organizerOpen || model.organizerSource != "" || model.organizerSelected != workstreamRowKey(web.ID) {
		t.Fatalf("organizer did not remain ready after move: open=%v source=%q selected=%q", model.organizerOpen, model.organizerSource, model.organizerSelected)
	}
}

func TestOrganizerUseReturnsWithSessionSelected(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	container := testWorkstream("018f0000-0000-4000-8000-00000000003c", "Core", []string{"/tmp/api", "/tmp/web"}, now)
	member := testDurableSession("018f0000-0000-4000-8000-00000000003d", container.ID, heikou.BackendCodex, "member", "/tmp/web", now)
	model.snapshot = control.Snapshot{Workstreams: []workstream.Workstream{container}, Sessions: []control.Session{member}}
	model.collapsed[workstreamRowKey(container.ID)] = true
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()
	model.openOrganizer()
	model.selectOrganizerKey(sessionRowKey(member))

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 'u', Text: "u"}))
	model = updated.(Model)
	if model.organizerOpen || model.selected != sessionRowKey(member) || model.collapsed[workstreamRowKey(container.ID)] {
		t.Fatalf("use did not return with visible session selected: open=%v selected=%q collapsed=%v", model.organizerOpen, model.selected, model.collapsed[workstreamRowKey(container.ID)])
	}
	if model.launchRoot() != "/tmp/web" {
		t.Fatalf("use did not align the launch root to selected session: %q", model.launchRoot())
	}
}

func TestOrganizerMoveExplicitlyAdoptsSelectedOrphan(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	orphan := testOrphan("018f0000-0000-4000-8000-00000000003b", "/tmp", time.Now())
	model.snapshot.Orphans = []control.Session{orphan}
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
		model.settingsOpen = true
		assertViewFits(t, model.View().Content, size.width, size.height)
		model.settingsOpen, model.organizerOpen = false, true
		assertViewFits(t, model.View().Content, size.width, size.height)
		model.organizerOpen, model.helpOpen = false, true
		assertViewFits(t, model.View().Content, size.width, size.height)
	}
}

func TestSelectedOrganizerRowsRemainValidAtNarrowWidths(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	now := time.Now()
	container := testWorkstream("018f0000-0000-4000-8000-000000000053", "Narrow", []string{"/tmp"}, now)
	session := testDurableSession("018f0000-0000-4000-8000-000000000054", container.ID, heikou.BackendCodex, "narrow row", "/tmp", now)
	model.snapshot = control.Snapshot{Workstreams: []workstream.Workstream{container}, Sessions: []control.Session{session}}
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
	model.organizerOpen = true
	assertMode("WORKSTREAM ORGANIZER", model.View().Content)
	model.organizerOpen, model.settingsOpen = false, true
	assertMode("SETTINGS", model.View().Content)
	model.settingsOpen, model.helpOpen = false, true
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
	model.snapshot = control.Snapshot{Workstreams: []workstream.Workstream{container}, Sessions: []control.Session{session}}
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

func TestOrganizerFooterExplainsEnterForSelectedNoun(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	container := testWorkstream("018f0000-0000-4000-8000-000000000056", "Core", []string{"/tmp"}, time.Now())
	session := testDurableSession("018f0000-0000-4000-8000-000000000057", container.ID, heikou.BackendCodex, "member", "/tmp", time.Now())
	model.snapshot = control.Snapshot{Workstreams: []workstream.Workstream{container}, Sessions: []control.Session{session}}
	model.selected = workstreamRowKey(container.ID)
	model.restoreSelection()
	model.openOrganizer()

	if help := model.organizerHelp(); !strings.Contains(help, "Enter expand/collapse") {
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
	if !model.helpOpen {
		t.Fatal("? on an empty composer did not open help")
	}
	plain := ansi.Strip(strings.Join(model.helpContentLines(), "\n"))
	for _, want := range []string{"local command center", "Nouns", "Workstream", "Orphaned"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("help is missing %q:\n%s", want, plain)
		}
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(Model)
	model.insertText("why")
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: '?', Text: "?"}))
	model = updated.(Model)
	if model.helpOpen || model.inputValue() != "why?" {
		t.Fatalf("printable ? changed mode/input: help=%v input=%q", model.helpOpen, model.inputValue())
	}
}

func TestHelpPanelScrolls(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height, model.helpOpen = 50, 12, true
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
	model.selected = sessionRowKey(session)
	model.restoreSelection()
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 'x', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if model.confirmDelete != session.ID {
		t.Fatal("delete confirmation was not armed")
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyF1}))
	model = updated.(Model)
	if model.confirmDelete != "" || !model.helpOpen {
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
	model.settings.ComposerKeys = config.ComposerKeys{
		NewSession: "ctrl+shift+n", SendMessage: "shift+enter", CycleRunner: "f6", CycleRoot: "alt+r",
	}
	model.insertText("custom launch")
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(Model)
	if cmd != nil || controller.startRequest.Prompt != "" {
		t.Fatal("default Enter still started a session after rebinding")
	}
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: 'n', Text: "N", Mod: tea.ModCtrl | tea.ModShift}))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("configured Ctrl-N did not start a session")
	}
	message := cmd()
	if controller.startRequest.Prompt != "custom launch" {
		t.Fatalf("start prompt = %q", controller.startRequest.Prompt)
	}
	updated, _ = model.Update(message)
	model = updated.(Model)

	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	model = updated.(Model)
	if model.backend != heikou.BackendCodex {
		t.Fatal("default Tab still cycled runner after rebinding")
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyF6}))
	if updated.(Model).backend != heikou.BackendClaude {
		t.Fatal("configured F6 did not cycle runner")
	}
}

func TestConfiguredSendKeyUsesComposerContext(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	model.settings.ComposerKeys = config.ComposerKeys{
		NewSession: "ctrl+n", SendMessage: "shift+enter", CycleRunner: "f6", CycleRoot: "alt+r",
	}
	session := testDurableSession("018f0000-0000-4000-8000-000000000055", "", heikou.BackendCodex, "original", "/tmp", time.Now())
	model.snapshot.Sessions = []control.Session{session}
	model.selected = sessionRowKey(session)
	model.restoreSelection()
	model.insertText("follow up")
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}))
	if cmd == nil {
		t.Fatal("configured Shift-Enter did not send")
	}
	_ = updated
	_ = cmd()
	if controller.sentSession != session.ID || controller.sentText != "follow up" {
		t.Fatalf("send = session %q text %q", controller.sentSession, controller.sentText)
	}
}

func TestSettingsRendersComposerBindings(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height, model.settingsOpen = 80, 24, true
	model.settings.ComposerKeys = config.ComposerKeys{
		NewSession: "ctrl+n", SendMessage: "shift+enter", CycleRunner: "f6", CycleRoot: "alt+r",
	}
	plain := ansi.Strip(model.View().Content)
	for _, want := range []string{"composer keys", "Ctrl+N", "Shift+Enter", "F6", "Alt+R"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("settings view is missing %q:\n%s", want, plain)
		}
	}
}

func TestSmallSettingsPaneScrollsToLaunchCommands(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height, model.settingsOpen = 40, 15, true
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
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: 'x', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if cmd != nil || model.confirmDelete != session.ID {
		t.Fatal("first Ctrl-X did not arm durable record deletion")
	}
	updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: 'x', Mod: tea.ModCtrl}))
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
	model.width, model.height, model.settingsOpen = 80, 24, true
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

func TestPasteNormalizesMultilineComposer(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	updated, _ := model.Update(tea.PasteMsg{Content: "first line\r\nsecond\tline\nthird"})
	if got := updated.(Model).inputValue(); got != "first line second line third" {
		t.Fatalf("input = %q", got)
	}
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
