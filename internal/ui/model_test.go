package ui

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/zamborg/heikou/internal/config"
	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/workstream"
)

type fakeController struct {
	snapshot     control.Snapshot
	startRequest control.StartRequest
	adoptSession string
	adoptTarget  string
}

func (f *fakeController) Snapshot(context.Context) (control.Snapshot, error) { return f.snapshot, nil }
func (f *fakeController) Find(context.Context, string) (control.Session, error) {
	return control.Session{}, nil
}
func (f *fakeController) Start(_ context.Context, request control.StartRequest) (control.Session, error) {
	f.startRequest = request
	return control.Session{ID: "018f0000-0000-4000-8000-000000000099", Backend: request.Backend, Prompt: request.Prompt, Root: request.Root, WorkstreamID: request.WorkstreamID, Durable: true, Status: control.StatusLive}, nil
}
func (f *fakeController) Send(context.Context, string, string) error { return nil }
func (f *fakeController) Capture(context.Context, string, int) (string, error) {
	return "", nil
}
func (f *fakeController) Stop(context.Context, string) error { return nil }
func (f *fakeController) AttachCommand(context.Context, string) (*exec.Cmd, error) {
	return exec.Command("true"), nil
}
func (f *fakeController) CreateWorkstream(context.Context, string, string, []string) (workstream.Workstream, error) {
	return workstream.Workstream{}, nil
}
func (f *fakeController) RenameWorkstream(context.Context, string, string) error { return nil }
func (f *fakeController) ArchiveWorkstream(context.Context, string) error        { return nil }
func (f *fakeController) MoveSession(context.Context, string, string) error      { return nil }

func (f *fakeController) AdoptSession(_ context.Context, sessionID, workstreamID string) (control.Session, error) {
	f.adoptSession, f.adoptTarget = sessionID, workstreamID
	return control.Session{}, nil
}
func (f *fakeController) AddRoot(context.Context, string, string) error { return nil }

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

func TestOrganizerMoveExplicitlyAdoptsSelectedOrphan(t *testing.T) {
	model, controller := newTestModel("/tmp", heikou.BackendCodex)
	orphan := testOrphan("018f0000-0000-4000-8000-000000000035", "/tmp", time.Now())
	model.snapshot.Orphans = []control.Session{orphan}
	model.selected = sessionRowKey(orphan)
	model.restoreSelection()
	model.openOrganizer()
	if model.organizerSource != orphan.ID {
		t.Fatal("orphan was not retained as organizer source")
	}
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
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
	for _, size := range []struct{ width, height int }{{40, 15}, {80, 24}, {120, 40}} {
		model, _ := newTestModel("/tmp", heikou.BackendCodex)
		model.width, model.height = size.width, size.height
		model.settingsOpen = true
		assertViewFits(t, model.View().Content, size.width, size.height)
		model.settingsOpen, model.organizerOpen = false, true
		assertViewFits(t, model.View().Content, size.width, size.height)
	}
}

func TestSettingsErrorsSurviveSnapshotRefresh(t *testing.T) {
	model, _ := newTestModel("/tmp", heikou.BackendCodex)
	model.width, model.height, model.settingsOpen = 80, 24, true
	updated, _ := model.Update(settingsMsg{err: context.DeadlineExceeded})
	model = updated.(Model)
	updated, _ = model.Update(snapshotMsg{snapshot: control.Snapshot{}})
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

func assertViewFits(t *testing.T, content string, width, height int) {
	t.Helper()
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
