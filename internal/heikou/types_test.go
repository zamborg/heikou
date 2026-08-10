package heikou

import (
	"testing"
	"time"
)

func TestSessionRuntimeFreezesAtExit(t *testing.T) {
	started := time.Unix(1_000, 0)
	ended := started.Add(42 * time.Second)
	session := Session{StartedAt: started, EndedAt: ended, Status: StatusExited}
	if got := session.Runtime(ended.Add(time.Hour)); got != 42*time.Second {
		t.Fatalf("Runtime() = %s, want 42s", got)
	}
}

func TestParseBackend(t *testing.T) {
	for _, value := range []string{"codex", "CODEX", " claude ", "no-agent", "NO-AGENT"} {
		if _, err := ParseBackend(value); err != nil {
			t.Fatalf("ParseBackend(%q): %v", value, err)
		}
	}
	if _, err := ParseBackend("other"); err == nil {
		t.Fatal("ParseBackend(other) succeeded, want error")
	}
}

func TestBackendNextCyclesAllModes(t *testing.T) {
	got := []Backend{
		BackendCodex.Next(),
		BackendClaude.Next(),
		BackendNoAgent.Next(),
	}
	want := []Backend{BackendClaude, BackendNoAgent, BackendCodex}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("cycle[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}
