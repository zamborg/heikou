package supervisor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/zamborg/heikou/internal/env"
	"github.com/zamborg/heikou/internal/format"
	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/runner"
)

const (
	DefaultSocket    = "heikou"
	bootstrapVersion = "1"
	framingAttempts  = 3
)

type Tmux struct {
	binary     string
	socket     string
	executable string
}

func New(socket string) (*Tmux, error) {
	if strings.TrimSpace(socket) == "" {
		socket = DefaultSocket
	}
	binary, err := exec.LookPath("tmux")
	if err != nil {
		return nil, fmt.Errorf("tmux is required: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate heikou executable: %w", err)
	}
	return &Tmux{binary: binary, socket: socket, executable: executable}, nil
}

func (t *Tmux) Socket() string { return t.socket }

// Bootstrap creates a configuration-isolated tmux server and sets global pane
// defaults before any runner can start. In particular, remain-on-exit must be
// installed before new-session or a fast failure could disappear immediately.
func (t *Tmux) Bootstrap(ctx context.Context) error {
	// These two options share the start-server invocation so a zero-session
	// server cannot exit, and a fast child cannot vanish, before configuration.
	if _, err := t.run(ctx, nil,
		"-f", "/dev/null", "start-server", ";",
		"set-option", "-s", "exit-empty", "off", ";",
		"set-option", "-gw", "remain-on-exit", "on",
	); err != nil {
		return fmt.Errorf("start tmux server: %w", err)
	}
	serverEnvironment, err := t.run(ctx, nil, "show-environment", "-g")
	if err != nil {
		return fmt.Errorf("read tmux server environment: %w", err)
	}
	// Include names retained by the long-lived server even when the current
	// client has unset them. tmux will then mark those names removed in the new
	// session instead of leaking a stale credential into future agents.
	environmentNames := currentEnvironmentNames(string(serverEnvironment))
	if _, err := t.run(ctx, nil, "set-option", "-g", "update-environment", strings.Join(environmentNames, " ")); err != nil {
		return fmt.Errorf("refresh tmux environment policy: %w", err)
	}
	marker, _ := t.run(ctx, nil, "show-options", "-sv", "@heikou_bootstrap_version")
	if strings.TrimSpace(string(marker)) == bootstrapVersion {
		return nil
	}

	commands := [][]string{
		{"set-option", "-s", "exit-empty", "off"},
		{"set-option", "-gw", "remain-on-exit", "on"},
		{"set-option", "-gw", "history-limit", "100000"},
		{"set-option", "-g", "status", "off"},
		{"set-option", "-g", "mouse", "on"},
		{"set-option", "-g", "focus-events", "on"},
		{"set-option", "-g", "set-clipboard", "on"},
		{"set-option", "-gw", "window-size", "latest"},
		{"set-option", "-s", "escape-time", "10"},
		{"set-option", "-gw", "allow-passthrough", "on"},
		{"set-option", "-s", "extended-keys", "on"},
		{"set-option", "-as", "terminal-features", ",xterm*:extkeys"},
		{"bind-key", "-n", "C-\\", "detach-client"},
		{"set-option", "-s", "@heikou_bootstrap_version", bootstrapVersion},
	}
	args := joinTmuxCommands(commands)
	if _, err := t.run(ctx, nil, args...); err != nil {
		return fmt.Errorf("configure tmux server: %w", err)
	}
	return nil
}

func (t *Tmux) Sessions(ctx context.Context) ([]heikou.Session, error) {
	formatFields := []string{
		"#{session_name}",
		"#{pane_id}",
		"#{@heikou_id}",
		"#{@heikou_pane}",
		"#{@heikou_canonical}",
		"#{@heikou_backend}",
		"#{@heikou_started}",
		"#{@heikou_prompt}",
		"#{@heikou_root}",
		"#{pane_dead}",
		"#{pane_dead_status}",
		"#{pane_dead_time}",
		"#{window_activity}",
		"#{pane_current_path}",
		"#{pane_current_command}",
		"#{session_attached}",
		"#{pane_in_mode}",
		"#{pane_input_off}",
		"#{@heikou_last_user_message}",
	}

	rows, err := t.listPaneFields(ctx, formatFields)
	if err != nil {
		if isMissingServer(err) || strings.Contains(err.Error(), "no current target") {
			return nil, nil
		}
		return nil, fmt.Errorf("list tmux panes: %w", err)
	}

	var sessions []heikou.Session
	for _, fields := range rows {
		if fields[2] == "" && strings.HasPrefix(fields[0], "h-") {
			candidate := strings.TrimPrefix(fields[0], "h-")
			if validSessionID(candidate) {
				fields[2] = candidate
			}
		}
		// @heikou_pane is the V0 marker. New sessions receive a pane-scoped
		// canonical marker in the same tmux command queue that creates them.
		if fields[2] == "" || (fields[3] != fields[1] && fields[4] != "1") {
			continue
		}
		session, parseErr := parseSession(fields)
		if parseErr == nil {
			sessions = append(sessions, session)
		}
	}

	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].Alive() != sessions[j].Alive() {
			return sessions[i].Alive()
		}
		return sessions[i].StartedAt.After(sessions[j].StartedAt)
	})
	return sessions, nil
}

func (t *Tmux) Find(ctx context.Context, query string) (heikou.Session, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return heikou.Session{}, errors.New("session id is required")
	}
	sessions, err := t.Sessions(ctx)
	if err != nil {
		return heikou.Session{}, err
	}
	var matches []heikou.Session
	for _, session := range sessions {
		if session.ID == query || session.Name == query {
			return session, nil
		}
		if strings.HasPrefix(session.ID, query) || strings.HasPrefix(session.Name, query) {
			matches = append(matches, session)
		}
	}
	if len(matches) == 0 {
		return heikou.Session{}, fmt.Errorf("no session matches %q", query)
	}
	if len(matches) > 1 {
		return heikou.Session{}, fmt.Errorf("session prefix %q is ambiguous", query)
	}
	return matches[0], nil
}

// RuntimeExists deliberately uses only the durable identity and stable tmux
// name. Sessions() is a rich projection and may omit a pane whose optional or
// partially-written metadata cannot be decoded; lifecycle deletion must not
// mistake that omission for proof that the tmux runtime is absent.
func (t *Tmux) RuntimeExists(ctx context.Context, id, boundName string) (bool, error) {
	id = strings.TrimSpace(id)
	boundName = strings.TrimSpace(boundName)
	if id == "" && boundName == "" {
		return false, errors.New("runtime id or bound name is required")
	}

	rows, err := t.listPaneFields(ctx, []string{"#{session_name}", "#{@heikou_id}"})
	if err != nil {
		if isMissingServer(err) || strings.Contains(err.Error(), "no current target") {
			return false, nil
		}
		return false, fmt.Errorf("check tmux runtime existence: %w", err)
	}

	defaultName := ""
	if id != "" {
		defaultName = "h-" + id
	}
	for _, fields := range rows {
		if (boundName != "" && fields[0] == boundName) ||
			(defaultName != "" && fields[0] == defaultName) ||
			(id != "" && fields[1] == id) {
			return true, nil
		}
	}
	return false, nil
}

// listPaneFields frames tmux's formatted output with printable, per-call
// sentinels. tmux 3.5 and older escape C0 control bytes in list-panes output,
// so a control character cannot safely delimit fields across supported tmux
// versions. A fresh nonce also keeps user-controlled paths from becoming
// ambiguous framing. Malformed output is retried with new sentinels.
func (t *Tmux) listPaneFields(ctx context.Context, formatFields []string) ([][]string, error) {
	if len(formatFields) == 0 {
		return nil, errors.New("at least one tmux format field is required")
	}
	var parseErr error
	for attempt := 0; attempt < framingAttempts; attempt++ {
		token, err := randomToken()
		if err != nil {
			return nil, err
		}
		fieldMarker := "__heikou_field_" + token + "__"
		recordMarker := "__heikou_record_" + token + "__"
		format := strings.Join(formatFields, fieldMarker) + recordMarker
		output, err := t.run(ctx, nil, "list-panes", "-a", "-F", format)
		if err != nil {
			return nil, err
		}
		rows, err := parsePaneFields(output, fieldMarker, recordMarker, len(formatFields))
		if err == nil {
			return rows, nil
		}
		parseErr = err
	}
	return nil, fmt.Errorf("decode tmux pane list after %d attempts: %w", framingAttempts, parseErr)
}

func parsePaneFields(output []byte, fieldMarker, recordMarker string, fieldCount int) ([][]string, error) {
	remaining := string(output)
	if remaining == "" {
		return nil, nil
	}

	var rows [][]string
	for remaining != "" {
		if len(rows) > 0 {
			switch {
			case strings.HasPrefix(remaining, "\r\n"):
				remaining = strings.TrimPrefix(remaining, "\r\n")
			case strings.HasPrefix(remaining, "\n"):
				remaining = strings.TrimPrefix(remaining, "\n")
			default:
				return nil, errors.New("tmux pane record is missing its line ending")
			}
			if remaining == "" {
				break
			}
		}

		record, rest, found := strings.Cut(remaining, recordMarker)
		if !found {
			return nil, errors.New("tmux pane record is missing its sentinel")
		}
		fields := strings.Split(record, fieldMarker)
		if len(fields) != fieldCount {
			return nil, fmt.Errorf("tmux pane record has %d fields, want %d", len(fields), fieldCount)
		}
		rows = append(rows, fields)
		remaining = rest
	}
	return rows, nil
}

func (t *Tmux) Start(ctx context.Context, request heikou.StartRequest) (heikou.Session, error) {
	if !validSessionID(request.ID) {
		return heikou.Session{}, fmt.Errorf("invalid caller-supplied session id %q", request.ID)
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return heikou.Session{}, errors.New("prompt cannot be empty")
	}
	if _, err := heikou.ParseBackend(string(request.Backend)); err != nil {
		return heikou.Session{}, err
	}
	var command []string
	if request.Backend != heikou.BackendNoAgent {
		var err error
		command, err = runner.ResolveCommand(request.Backend, request.Command)
		if err != nil {
			return heikou.Session{}, err
		}
	}
	root, err := filepath.Abs(request.Root)
	if err != nil {
		return heikou.Session{}, fmt.Errorf("resolve root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return heikou.Session{}, fmt.Errorf("open root %q: %w", root, err)
	}
	if !info.IsDir() {
		return heikou.Session{}, fmt.Errorf("root %q is not a directory", root)
	}
	if err := t.Bootstrap(ctx); err != nil {
		return heikou.Session{}, err
	}

	id := request.ID
	name := "h-" + id
	title := titleFor(request.Prompt, id)
	started := time.Now()
	newSession := []string{
		"new-session", "-d", "-P", "-F", "#{pane_id}",
		"-s", name, "-n", string(request.Backend), "-c", root,
		"-x", "120", "-y", "36",
		"-e", env.SessionID + "=" + id,
	}
	if request.Backend != heikou.BackendNoAgent {
		encodedCommand, err := runner.EncodeCommand(command)
		if err != nil {
			return heikou.Session{}, fmt.Errorf("encode %s runner command: %w", request.Backend, err)
		}
		newSession = append(newSession,
			t.executable, "__agent", string(request.Backend),
			runner.Encode(request.Prompt), runner.Encode(title), encodedCommand,
		)
	}
	commands := [][]string{
		newSession,
		{"set-option", "-t", name, "@heikou_id", id},
		{"set-option", "-p", "-t", name + ":0.0", "@heikou_canonical", "1"},
		{"set-option", "-t", name, "@heikou_backend", string(request.Backend)},
		{"set-option", "-t", name, "@heikou_started", strconv.FormatInt(started.Unix(), 10)},
		{"set-option", "-t", name, "@heikou_prompt", runner.Encode(request.Prompt)},
		{"set-option", "-t", name, "@heikou_root", runner.Encode(root)},
	}
	output, err := t.run(ctx, nil, joinTmuxCommands(commands)...)
	if err != nil {
		if recovered, ok := t.findAfterAmbiguousStart(id); ok {
			return recovered, nil
		}
		return heikou.Session{}, fmt.Errorf("spawn %s session: %w", request.Backend, err)
	}
	paneID := strings.TrimSpace(string(output))
	if paneID == "" {
		if recovered, ok := t.findAfterAmbiguousStart(id); ok {
			return recovered, nil
		}
		return heikou.Session{}, errors.New("tmux did not return a pane id")
	}

	return heikou.Session{
		ID: id, Name: name, PaneID: paneID, Backend: request.Backend,
		Prompt: request.Prompt, Root: root, CurrentPath: root,
		Status: heikou.StatusLive, StartedAt: started,
	}, nil
}

func (t *Tmux) Send(ctx context.Context, session heikou.Session, message string) error {
	if strings.TrimSpace(message) == "" {
		return errors.New("message cannot be empty")
	}
	current, err := t.Find(ctx, session.ID)
	if err != nil {
		return err
	}
	if !current.Alive() {
		return fmt.Errorf("session %s has exited", format.ShortID(current.ID))
	}
	if current.PaneInMode {
		return fmt.Errorf("session %s is in a tmux mode; attach and leave that mode before sending", format.ShortID(current.ID))
	}
	if current.InputDisabled {
		return fmt.Errorf("session %s has tmux input disabled", format.ShortID(current.ID))
	}

	bufferID, err := randomToken()
	if err != nil {
		return err
	}
	bufferName := "heikou-" + bufferID
	if _, err := t.run(ctx, []byte(message), "load-buffer", "-b", bufferName, "-"); err != nil {
		return fmt.Errorf("load message buffer: %w", err)
	}
	// Use tmux's terminal-native LF-to-CR conversion so every logical newline
	// is accepted by the pane line discipline, including on tmux 3.4.
	if _, err := t.run(ctx, nil, "paste-buffer", "-d", "-p", "-b", bufferName, "-t", current.PaneID); err != nil {
		_, _ = t.run(context.Background(), nil, "delete-buffer", "-b", bufferName)
		return fmt.Errorf("paste message: %w", err)
	}
	if _, err := t.run(ctx, nil, "send-keys", "-t", current.PaneID, "Enter"); err != nil {
		return fmt.Errorf("submit message: %w", err)
	}
	// Delivery has already succeeded, so presentation metadata is best effort.
	// Returning an error here could encourage the caller to resend the message.
	if preview := userMessagePreview(message); preview != "" {
		_, _ = t.run(ctx, nil, "set-option", "-t", current.Name, "@heikou_last_user_message", runner.Encode(preview))
	}
	return nil
}

func (t *Tmux) Capture(ctx context.Context, session heikou.Session, lines int) (string, error) {
	if lines <= 0 {
		lines = 80
	}
	output, err := t.run(ctx, nil, "capture-pane", "-p", "-J", "-S", fmt.Sprintf("-%d", lines), "-t", session.PaneID)
	if err != nil {
		return "", fmt.Errorf("capture session: %w", err)
	}
	return cleanCapture(string(output)), nil
}

func (t *Tmux) Stop(ctx context.Context, session heikou.Session) error {
	if err := t.killByName(ctx, session.Name); err != nil {
		return fmt.Errorf("stop tmux runtime %s: %w", format.ShortID(session.ID), err)
	}
	return nil
}

func (t *Tmux) AttachCommand(session heikou.Session) *exec.Cmd {
	command := exec.Command(t.binary, "-L", t.socket, "attach-session", "-t", session.Name)
	command.Env = withoutNestedTmux(os.Environ())
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command
}

func (t *Tmux) killByName(ctx context.Context, name string) error {
	_, err := t.run(ctx, nil, "kill-session", "-t", name)
	return err
}

func (t *Tmux) findAfterAmbiguousStart(id string) (heikou.Session, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session, err := t.Find(ctx, id)
	return session, err == nil
}

func (t *Tmux) run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	fullArgs := append([]string{"-L", t.socket}, args...)
	command := exec.CommandContext(ctx, t.binary, fullArgs...)
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("%s", message)
	}
	return stdout.Bytes(), nil
}

func parseSession(fields []string) (heikou.Session, error) {
	backend, err := heikou.ParseBackend(fields[5])
	if err != nil {
		return heikou.Session{}, err
	}
	startedUnix, err := strconv.ParseInt(fields[6], 10, 64)
	if err != nil {
		return heikou.Session{}, err
	}
	prompt, err := decodeMetadata(fields[7])
	if err != nil {
		return heikou.Session{}, err
	}
	root, err := decodeMetadata(fields[8])
	if err != nil {
		return heikou.Session{}, err
	}
	// This option is optional presentation metadata. A malformed value must not
	// hide an otherwise valid runtime from discovery.
	lastUserMessage, _ := decodeMetadata(fields[18])
	deadUnix, _ := strconv.ParseInt(fields[11], 10, 64)
	activityUnix, _ := strconv.ParseInt(fields[12], 10, 64)
	attached, _ := strconv.Atoi(fields[15])
	paneModeCount, _ := strconv.Atoi(fields[16])

	status := heikou.StatusLive
	var exitCode *int
	if fields[9] == "1" {
		status = heikou.StatusExited
		if fields[10] != "" {
			code, err := strconv.Atoi(fields[10])
			if err != nil {
				return heikou.Session{}, fmt.Errorf("parse pane exit status %q: %w", fields[10], err)
			}
			exitCode = &code
			if code != 0 {
				status = heikou.StatusFailed
			}
		}
	}
	return heikou.Session{
		Name: fields[0], PaneID: fields[1], ID: fields[2], Backend: backend,
		Prompt: prompt, LastUserMessage: lastUserMessage, Root: root, Status: status,
		StartedAt: time.Unix(startedUnix, 0), EndedAt: unixTime(deadUnix),
		LastActivityAt: unixTime(activityUnix), ExitCode: exitCode,
		CurrentPath: fields[13], CurrentCommand: fields[14],
		AttachedClients: attached, PaneInMode: paneModeCount != 0,
		InputDisabled: fields[17] == "1",
	}, nil
}

func userMessagePreview(message string) string {
	message = ansi.Strip(message)
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
	message = strings.Join(strings.Fields(message), " ")
	runes := []rune(message)
	if len(runes) > 280 {
		message = string(runes[:280])
	}
	return message
}

func decodeMetadata(value string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", fmt.Errorf("decode session metadata: %w", err)
	}
	return string(decoded), nil
}

func randomToken() (string, error) {
	var value [5]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func validSessionID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, r := range value {
		switch index {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
				return false
			}
		}
	}
	return true
}

func titleFor(prompt, id string) string {
	prompt = ansi.Strip(prompt)
	prompt = strings.Map(func(r rune) rune {
		if !unicode.IsControl(r) || unicode.IsSpace(r) {
			return r
		}
		return -1
	}, prompt)
	words := strings.Fields(prompt)
	if len(words) > 5 {
		words = words[:5]
	}
	title := strings.Join(words, " ")
	if len([]rune(title)) > 36 {
		title = string([]rune(title)[:36])
	}
	if title == "" {
		title = "heikou " + format.ShortID(id)
	}
	return title
}

func unixTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(value, 0)
}

func joinTmuxCommands(commands [][]string) []string {
	var result []string
	for index, command := range commands {
		if index > 0 {
			result = append(result, ";")
		}
		result = append(result, command...)
	}
	return result
}

func isMissingServer(err error) bool {
	message := err.Error()
	return strings.Contains(message, "no server running") ||
		strings.Contains(message, "No such file or directory") ||
		strings.Contains(message, "error connecting")
}

func withoutNestedTmux(environment []string) []string {
	result := make([]string, 0, len(environment))
	termFound := false
	for _, entry := range environment {
		if strings.HasPrefix(entry, "TMUX=") || strings.HasPrefix(entry, "TMUX_PANE=") {
			continue
		}
		if strings.HasPrefix(entry, "TERM=") {
			termFound = true
			if entry == "TERM=" || entry == "TERM=dumb" {
				entry = "TERM=xterm-256color"
			}
		}
		result = append(result, entry)
	}
	if !termFound {
		result = append(result, "TERM=xterm-256color")
	}
	return result
}

func cleanCapture(value string) string {
	lines := strings.Split(strings.ReplaceAll(value, "\r", ""), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "Pane is dead (") {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// baselineEnvironmentNames are the variables the long-lived tmux server must
// re-read from each client rather than remember, so that a credential the user
// has since removed cannot survive into a later session.
//
// Only the runner overrides are listed from Heikou's own set. The rest are read
// by the h process rather than by an agent, and HEIKOU_SESSION_ID is written
// per session with new-session -e: refreshing that one from the attaching
// client would replace a session's own identity with the identity of whoever
// attached.
var baselineEnvironmentNames = []string{
	"DISPLAY", "KRB5CCNAME", "SSH_ASKPASS", "SSH_AUTH_SOCK", "SSH_AGENT_PID",
	"SSH_CONNECTION", "WINDOWID", "XAUTHORITY", "PATH", "SHELL", "USER",
	"LOGNAME", "LANG", "LC_ALL", "LC_CTYPE", "COLORTERM", "TERM_PROGRAM",
	"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_VERTEX", "AWS_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION",
	"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
	"GOOGLE_APPLICATION_CREDENTIALS", "CLOUD_ML_REGION",
	"ANTHROPIC_VERTEX_PROJECT_ID", "AZURE_OPENAI_API_KEY",
	env.CodexBinary, env.ClaudeBinary,
}

func currentEnvironmentNames(serverEnvironment string) []string {
	names := make(map[string]struct{}, len(os.Environ())+len(baselineEnvironmentNames))
	for _, name := range baselineEnvironmentNames {
		names[name] = struct{}{}
	}
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || !safeEnvironmentName(name) {
			continue
		}
		names[name] = struct{}{}
	}
	for _, entry := range strings.Split(serverEnvironment, "\n") {
		entry = strings.TrimPrefix(entry, "-")
		name, _, _ := strings.Cut(entry, "=")
		if !safeEnvironmentName(name) {
			continue
		}
		names[name] = struct{}{}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func safeEnvironmentName(value string) bool {
	if !validEnvironmentName(value) {
		return false
	}
	switch value {
	case "TMUX", "TMUX_PANE", "TERM", "PWD", "OLDPWD", "SHLVL", "_":
		return false
	default:
		return true
	}
}

func validEnvironmentName(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || (index > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}
