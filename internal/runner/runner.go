package runner

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/zamborg/heikou/internal/env"
	"github.com/zamborg/heikou/internal/heikou"
)

// Launch is everything an adapter needs to build a runner's argv. It is a
// struct rather than a parameter list because Resume is the fourth thing that
// varies and a fourth bare string argument would be one nobody could read at
// the call site.
type Launch struct {
	Prompt string
	Title  string
	// SessionID is Heikou's durable id for the session being started. Claude
	// accepts it as its own conversation id, which is what makes a fresh Claude
	// session's conversation known without asking anyone.
	SessionID string
	// Resume names a native conversation to continue instead of beginning one.
	// Empty starts fresh. When it is set the runner, not Heikou, owns the
	// resulting conversation id — it is the id being resumed.
	Resume string
}

// Resuming reports whether this launch continues an existing conversation.
func (l Launch) Resuming() bool { return strings.TrimSpace(l.Resume) != "" }

type Adapter interface {
	Backend() heikou.Backend
	Binary() string
	Arguments(Launch) []string
	// SupportsResume reports whether this runner can continue a conversation by
	// id. A runner that cannot must refuse the request rather than silently
	// starting a fresh session that looks resumed.
	SupportsResume() bool
}

type codexAdapter struct{}

func (codexAdapter) Backend() heikou.Backend { return heikou.BackendCodex }
func (codexAdapter) Binary() string {
	return env.ValueOr(env.CodexBinary, "codex")
}
func (codexAdapter) SupportsResume() bool { return true }

// Arguments builds Codex argv. Codex has no flag for choosing a session id at
// launch, so a fresh session carries no identity Heikou chose; `codex resume`
// takes the id Codex minted as a positional argument.
func (codexAdapter) Arguments(launch Launch) []string {
	if launch.Resuming() {
		return []string{"resume", launch.Resume, "--", launch.Prompt}
	}
	return []string{"--", launch.Prompt}
}

type claudeAdapter struct{}

func (claudeAdapter) Backend() heikou.Backend { return heikou.BackendClaude }
func (claudeAdapter) Binary() string {
	return env.ValueOr(env.ClaudeBinary, "claude")
}
func (claudeAdapter) SupportsResume() bool { return true }

// Arguments builds Claude argv. --session-id and --resume are mutually
// exclusive: the first names a conversation to create, the second names one
// that already exists, and passing both asks Claude to do two different things
// with one identity.
func (claudeAdapter) Arguments(launch Launch) []string {
	if launch.Resuming() {
		return []string{"--resume", launch.Resume, "--name", launch.Title, "--", launch.Prompt}
	}
	return []string{"--session-id", launch.SessionID, "--name", launch.Title, "--", launch.Prompt}
}

func AdapterFor(backend heikou.Backend) (Adapter, error) {
	switch backend {
	case heikou.BackendCodex:
		return codexAdapter{}, nil
	case heikou.BackendClaude:
		return claudeAdapter{}, nil
	default:
		return nil, fmt.Errorf("unsupported runner %q", backend)
	}
}

// ResolveCommand snapshots the executable and fixed arguments before tmux is
// involved. This keeps dashboard and CLI launches consistent even when the
// private tmux server has an older PATH. The macOS application candidates
// cover Codex installations bundled with the desktop app but not linked into
// the user's login-shell PATH.
func ResolveCommand(backend heikou.Backend, configured []string) ([]string, error) {
	adapter, err := AdapterFor(backend)
	if err != nil {
		return nil, err
	}
	command := append([]string(nil), configured...)
	if len(command) == 0 {
		command = []string{adapter.Binary()}
	}
	if strings.TrimSpace(command[0]) == "" {
		return nil, fmt.Errorf("find %s runner: command executable is empty", backend)
	}
	path, err := exec.LookPath(command[0])
	if err != nil && command[0] == "codex" && backend == heikou.BackendCodex {
		path, err = firstExecutable(codexAppCandidates())
	}
	if err != nil {
		return nil, fmt.Errorf("find %s runner %q: %w", backend, command[0], err)
	}
	if !filepath.IsAbs(path) {
		path, err = filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve %s runner %q: %w", backend, command[0], err)
		}
	}
	command[0] = path
	return command, nil
}

func codexAppCandidates() []string {
	paths := []string{
		"/Applications/ChatGPT.app/Contents/Resources/codex",
		"/Applications/Codex.app/Contents/Resources/codex",
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths,
			filepath.Join(home, "Applications", "ChatGPT.app", "Contents", "Resources", "codex"),
			filepath.Join(home, "Applications", "Codex.app", "Contents", "Resources", "codex"),
		)
	}
	return paths
}

func firstExecutable(candidates []string) (string, error) {
	var lastErr error
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate)
		if err == nil {
			return path, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = exec.ErrNotFound
	}
	return "", lastErr
}

// ExecEncoded is the hidden child-process entry point launched directly by
// tmux. Prompt, title and resume target are encoded only to keep tmux argv
// metadata compact; no value is ever evaluated by a shell.
func ExecEncoded(backendValue, encodedPrompt, encodedTitle, encodedCommand, encodedResume string) error {
	backend, err := heikou.ParseBackend(backendValue)
	if err != nil {
		return err
	}
	prompt, err := decode(encodedPrompt)
	if err != nil {
		return fmt.Errorf("decode prompt: %w", err)
	}
	title, err := decode(encodedTitle)
	if err != nil {
		return fmt.Errorf("decode title: %w", err)
	}
	resume, err := decode(encodedResume)
	if err != nil {
		return fmt.Errorf("decode resume target: %w", err)
	}

	adapter, err := AdapterFor(backend)
	if err != nil {
		return err
	}
	launch := Launch{
		Prompt: prompt, Title: title,
		SessionID: os.Getenv(env.SessionID), Resume: resume,
	}
	if launch.Resuming() && !adapter.SupportsResume() {
		return fmt.Errorf("runner %s cannot resume a conversation", backend)
	}
	command, err := DecodeCommand(encodedCommand)
	if err != nil {
		return fmt.Errorf("decode runner command: %w", err)
	}
	if len(command) == 0 {
		return errors.New("runner command cannot be empty")
	}
	path, err := exec.LookPath(command[0])
	if err != nil {
		return fmt.Errorf("find %s runner %q: %w", backend, command[0], err)
	}
	args := append([]string{path}, command[1:]...)
	args = append(args, adapter.Arguments(launch)...)
	return syscall.Exec(path, args, os.Environ())
}

func Encode(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func EncodeCommand(command []string) (string, error) {
	data, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	return Encode(string(data)), nil
}

func DecodeCommand(value string) ([]string, error) {
	decoded, err := decode(value)
	if err != nil {
		return nil, err
	}
	var command []string
	if err := json.Unmarshal([]byte(decoded), &command); err != nil {
		return nil, err
	}
	return command, nil
}

func decode(value string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return string(decoded), err
}
