package transcript

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zamborg/heikou/internal/heikou"
)

var launchedAt = time.Date(2026, 8, 13, 14, 40, 25, 0, time.UTC)

// rollout writes one Codex-shaped rollout file into the day directory for at,
// and returns the sessions root it was written under. Passing the same root
// twice appends another rollout to the same store.
func rollout(t *testing.T, sessions, id, cwd string, at time.Time, prompt string) string {
	t.Helper()
	if sessions == "" {
		sessions = t.TempDir()
	}
	day := filepath.Join(sessions,
		at.Format("2006"), at.Format("01"), at.Format("02"))
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatalf("create day directory: %v", err)
	}

	lines := []string{
		encode(t, map[string]any{
			"timestamp": at.Format(time.RFC3339Nano),
			"type":      "session_meta",
			"payload": map[string]any{
				"id": id, "cwd": cwd, "timestamp": at.Format(time.RFC3339Nano),
				"source": "cli", "originator": "codex-tui",
			},
		}),
		encode(t, map[string]any{"type": "event_msg", "payload": map[string]any{"type": "task_started"}}),
		// Codex injects developer messages and an environment block under the
		// user role before the person's first word. A matcher that trusts the
		// role compares the launch prompt against this and never matches.
		encode(t, map[string]any{
			"type": "response_item",
			"payload": map[string]any{
				"type": "message", "role": "developer",
				"content": []map[string]any{{"type": "input_text", "text": "<permissions instructions>"}},
			},
		}),
		encode(t, map[string]any{
			"type": "response_item",
			"payload": map[string]any{
				"type": "message", "role": "user",
				"content": []map[string]any{{"type": "input_text", "text": "<environment_context>\n  <cwd>" + cwd + "</cwd>\n</environment_context>"}},
			},
		}),
		encode(t, map[string]any{
			"type": "response_item",
			"payload": map[string]any{
				"type": "message", "role": "user",
				"content": []map[string]any{{"type": "input_text", "text": prompt}},
			},
		}),
	}
	name := "rollout-" + at.Format("2006-01-02T15-04-05") + "-" + id + ".jsonl"
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(day, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
	return sessions
}

func encode(t *testing.T, value map[string]any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode record: %v", err)
	}
	return string(data)
}

func find(sessions, root, prompt string, at time.Time) (Conversation, error) {
	return Reader{CodexSessions: sessions}.FindConversation(ConversationRequest{
		Runner: heikou.BackendCodex, Root: root, StartedAt: at, Prompt: prompt,
	})
}

func TestCodexConversationIsFoundFromLaunchDirectoryTimeAndPrompt(t *testing.T) {
	sessions := rollout(t, "", "019e6d0c-14bd-7792-91d2-f684a8dc6e80", "/work/repo", launchedAt.Add(2*time.Second), "ship the release")
	got, err := find(sessions, "/work/repo", "ship the release", launchedAt)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "019e6d0c-14bd-7792-91d2-f684a8dc6e80" {
		t.Fatalf("conversation id = %q", got.ID)
	}
	if got.Path == "" {
		t.Fatal("match did not report the file it came from")
	}
}

// This is the case the whole prompt-matching design exists for. Running several
// agents in one repository at once is what Heikou is for, so two rollouts
// minutes apart in the same directory is the normal case, not the exotic one.
func TestConcurrentSessionsInOneDirectoryAreToldApartByTheirPrompt(t *testing.T) {
	sessions := rollout(t, "", "aaaaaaaa-0000-7000-8000-000000000001", "/work/repo", launchedAt.Add(3*time.Second), "fix the parser")
	sessions = rollout(t, sessions, "bbbbbbbb-0000-7000-8000-000000000002", "/work/repo", launchedAt.Add(9*time.Second), "write the docs")

	got, err := find(sessions, "/work/repo", "write the docs", launchedAt)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "bbbbbbbb-0000-7000-8000-000000000002" {
		t.Fatalf("conversation id = %q, want the rollout whose prompt matches", got.ID)
	}
}

// Two launches that agree on everything Heikou can check are genuinely
// indistinguishable. Choosing the nearer one would resume the wrong work while
// reporting the same confidence as a real match.
func TestIdenticalLaunchesRefuseRatherThanChoose(t *testing.T) {
	sessions := rollout(t, "", "aaaaaaaa-0000-7000-8000-000000000001", "/work/repo", launchedAt.Add(3*time.Second), "retry it")
	sessions = rollout(t, sessions, "bbbbbbbb-0000-7000-8000-000000000002", "/work/repo", launchedAt.Add(20*time.Second), "retry it")

	if _, err := find(sessions, "/work/repo", "retry it", launchedAt); !errors.Is(err, ErrConversationAmbiguous) {
		t.Fatalf("error = %v, want ErrConversationAmbiguous", err)
	}
}

func TestConversationIsNotFoundWhenNothingMatches(t *testing.T) {
	sessions := rollout(t, "", "aaaaaaaa-0000-7000-8000-000000000001", "/other/repo", launchedAt.Add(2*time.Second), "ship the release")

	tests := []struct {
		name         string
		root, prompt string
		at           time.Time
	}{
		{name: "different directory", root: "/work/repo", prompt: "ship the release", at: launchedAt},
		{name: "different prompt", root: "/other/repo", prompt: "something else", at: launchedAt},
		{
			name: "outside the window", root: "/other/repo", prompt: "ship the release",
			at: launchedAt.Add(-2 * CodexMatchWindow),
		},
		{
			name: "rollout predates the launch", root: "/other/repo", prompt: "ship the release",
			at: launchedAt.Add(time.Hour),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := find(sessions, test.root, test.prompt, test.at); !errors.Is(err, ErrConversationNotFound) {
				t.Fatalf("error = %v, want ErrConversationNotFound", err)
			}
		})
	}
}

// Codex partitions rollouts by local date but stamps records in UTC, so a
// launch near midnight files itself in a directory the launch date does not
// name. The scan covers the neighbouring days rather than reasoning about which
// convention applies at a given offset.
func TestALaunchNearMidnightIsFoundInTheNeighbouringDayDirectory(t *testing.T) {
	at := time.Date(2026, 8, 13, 23, 59, 50, 0, time.UTC)
	sessions := rollout(t, "", "cccccccc-0000-7000-8000-000000000003", "/work/repo", at.Add(30*time.Second), "cross midnight")

	got, err := find(sessions, "/work/repo", "cross midnight", at)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "cccccccc-0000-7000-8000-000000000003" {
		t.Fatalf("conversation id = %q", got.ID)
	}
}

func TestAbsentSessionsDirectoryIsNotFoundRatherThanAnError(t *testing.T) {
	sessions := filepath.Join(t.TempDir(), "never-created")
	if _, err := find(sessions, "/work/repo", "anything", launchedAt); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("error = %v, want ErrConversationNotFound", err)
	}
}

// Claude accepts --session-id, so its conversation is whatever Heikou chose.
// Looking it up on disk would replace a certainty with an inference, and the
// only way to keep that from happening quietly is to refuse the question.
func TestRunnersThatNameTheirOwnConversationAreRefused(t *testing.T) {
	for _, backend := range []heikou.Backend{heikou.BackendClaude, heikou.BackendNoAgent} {
		t.Run(string(backend), func(t *testing.T) {
			_, err := Reader{CodexSessions: t.TempDir()}.FindConversation(ConversationRequest{
				Runner: backend, Root: "/work/repo", StartedAt: launchedAt, Prompt: "hello",
			})
			if err == nil || !strings.Contains(err.Error(), "does not mint its own conversation id") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestIncompleteConversationRequestsAreRejected(t *testing.T) {
	tests := []struct {
		name    string
		request ConversationRequest
	}{
		{name: "no root", request: ConversationRequest{Runner: heikou.BackendCodex, StartedAt: launchedAt, Prompt: "x"}},
		{name: "no start", request: ConversationRequest{Runner: heikou.BackendCodex, Root: "/work", Prompt: "x"}},
		{name: "no prompt", request: ConversationRequest{Runner: heikou.BackendCodex, Root: "/work", StartedAt: launchedAt}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := (Reader{CodexSessions: t.TempDir()}).FindConversation(test.request); err == nil {
				t.Fatal("an incomplete request was accepted")
			}
		})
	}
}
