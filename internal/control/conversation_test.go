package control

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/workstream"
)

// startingSupervisor accepts every launch and records what it was asked to run,
// so a test can assert on the resume target that reached the runtime boundary.
type startingSupervisor struct {
	fakeSupervisor
	mu       sync.Mutex
	requests []heikou.StartRequest
}

func newStartingSupervisor() *startingSupervisor {
	supervisor := &startingSupervisor{}
	supervisor.start = func(request heikou.StartRequest) (heikou.Session, error) {
		supervisor.mu.Lock()
		supervisor.requests = append(supervisor.requests, request)
		supervisor.mu.Unlock()

		session := heikou.Session{
			ID: request.ID, Name: "h-" + request.ID, Backend: request.Backend,
			Prompt: request.Prompt, Root: request.Root,
			Status: heikou.StatusLive, StartedAt: time.Now(),
		}
		supervisor.fakeSupervisor.mu.Lock()
		supervisor.sessions = append(supervisor.sessions, session)
		supervisor.fakeSupervisor.mu.Unlock()
		return session, nil
	}
	return supervisor
}

func (s *startingSupervisor) lastRequest(t *testing.T) heikou.StartRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatal("nothing was started")
	}
	return s.requests[len(s.requests)-1]
}

func conversationController(t *testing.T, resolver ConversationResolver) (*Controller, *startingSupervisor, *memoryRepository) {
	t.Helper()
	repository := newMemoryRepository(t.TempDir())
	supervisor := newStartingSupervisor()
	options := []controllerOption{}
	if resolver != nil {
		options = append(options, WithConversationResolver(resolver))
	}
	return New(supervisor, repository, "heikou-test", options...), supervisor, repository
}

func mustStart(t *testing.T, controller *Controller, backend heikou.Backend, prompt string) Session {
	t.Helper()
	session, err := controller.Start(context.Background(), StartRequest{
		Backend: backend, Prompt: prompt, Root: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

// Heikou launches Claude as `claude --session-id <durable id>`, so the
// conversation is known the moment the launch succeeds. Registering it from
// what Heikou passed — rather than by looking for a file Claude may not have
// written yet — is what makes this certain instead of racy.
func TestAClaudeLaunchRegistersItsConversationWithoutAskingAnyone(t *testing.T) {
	controller, _, repository := conversationController(t, ConversationResolver(nil))
	session := mustStart(t, controller, heikou.BackendClaude, "start the work")

	state, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	record, ok := state.Session(session.ID)
	if !ok {
		t.Fatal("the session was not recorded")
	}
	conversation := record.Conversation
	if conversation == nil {
		t.Fatal("a claude launch did not register its conversation")
	}
	if conversation.ID != session.ID {
		t.Fatalf("conversation id = %q, want the durable session id %q", conversation.ID, session.ID)
	}
	if conversation.Source != workstream.ConversationAssigned {
		t.Fatalf("conversation source = %q, want assigned", conversation.Source)
	}
	if conversation.RecordedAt.IsZero() {
		t.Fatal("conversation was recorded without a time")
	}
}

// Codex has no flag for choosing a session id, so a fresh Codex launch has no
// conversation Heikou can state. Recording one anyway — the durable id, say —
// would be a value that resumes nothing while looking exactly like one that does.
func TestACodexLaunchRegistersNothingBecauseCodexNamesItsOwnConversation(t *testing.T) {
	controller, _, repository := conversationController(t, ConversationResolver(nil))
	session := mustStart(t, controller, heikou.BackendCodex, "start the work")

	state, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	record, _ := state.Session(session.ID)
	if record.Conversation != nil {
		t.Fatalf("a codex launch invented a conversation: %#v", record.Conversation)
	}
}

func TestRegisteringACodexConversationRecordsItAsObserved(t *testing.T) {
	calls := 0
	resolver := ResolveConversationFunc(func(_ context.Context, record workstream.SessionRecord) (string, error) {
		calls++
		if record.Backend != heikou.BackendCodex {
			t.Fatalf("resolver asked about runner %q", record.Backend)
		}
		return "019e6d0c-14bd-7792-91d2-f684a8dc6e80", nil
	})
	controller, _, repository := conversationController(t, resolver)
	session := mustStart(t, controller, heikou.BackendCodex, "start the work")

	conversation, err := controller.RegisterConversation(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if conversation.ID != "019e6d0c-14bd-7792-91d2-f684a8dc6e80" ||
		conversation.Source != workstream.ConversationObserved {
		t.Fatalf("conversation = %#v", conversation)
	}

	state, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	record, _ := state.Session(session.ID)
	if record.Conversation == nil || record.Conversation.ID != conversation.ID {
		t.Fatalf("registration was not persisted: %#v", record.Conversation)
	}

	// Idempotence is not just an optimisation. The resolver matches against
	// files Codex owns, and those come and go, so a second look could return a
	// different answer. The first id that could be proven is the one that keeps.
	again, err := controller.RegisterConversation(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again != conversation {
		t.Fatalf("second registration = %#v, want the first %#v", again, conversation)
	}
	if calls != 1 {
		t.Fatalf("resolver ran %d times, want 1", calls)
	}
}

// A Claude session already carries its conversation, so registering it must not
// reach the resolver at all. Consulting the filesystem for something Heikou
// chose is how a certainty quietly becomes an inference.
func TestRegisteringAClaudeConversationNeverConsultsTheResolver(t *testing.T) {
	resolver := ResolveConversationFunc(func(context.Context, workstream.SessionRecord) (string, error) {
		t.Fatal("the resolver was consulted for a runner that names its own conversation")
		return "", nil
	})
	controller, _, _ := conversationController(t, resolver)
	session := mustStart(t, controller, heikou.BackendClaude, "start the work")

	conversation, err := controller.RegisterConversation(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if conversation.Source != workstream.ConversationAssigned || conversation.ID != session.ID {
		t.Fatalf("conversation = %#v", conversation)
	}
}

// A resolver that cannot prove which conversation belongs to a session must
// leave the record alone. A half-written registration is worse than none: the
// next reader cannot tell it from one that was verified.
func TestAnUnresolvableConversationIsNotRecorded(t *testing.T) {
	refusal := errors.New("more than one codex rollout matches this launch")
	resolver := ResolveConversationFunc(func(context.Context, workstream.SessionRecord) (string, error) {
		return "", refusal
	})
	controller, _, repository := conversationController(t, resolver)
	session := mustStart(t, controller, heikou.BackendCodex, "start the work")

	if _, err := controller.RegisterConversation(context.Background(), session.ID); !errors.Is(err, refusal) {
		t.Fatalf("error = %v, want the resolver's refusal", err)
	}
	state, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	record, _ := state.Session(session.ID)
	if record.Conversation != nil {
		t.Fatalf("a refused registration was recorded anyway: %#v", record.Conversation)
	}
}

func TestAnEmptyResolvedConversationIsRejected(t *testing.T) {
	resolver := ResolveConversationFunc(func(context.Context, workstream.SessionRecord) (string, error) {
		return "   ", nil
	})
	controller, _, _ := conversationController(t, resolver)
	session := mustStart(t, controller, heikou.BackendCodex, "start the work")

	if _, err := controller.RegisterConversation(context.Background(), session.ID); err == nil {
		t.Fatal("an empty conversation id was accepted")
	}
}

func TestANoAgentSessionHasNoConversationToRegister(t *testing.T) {
	controller, _, _ := conversationController(t, ConversationResolver(nil))
	session := mustStart(t, controller, heikou.BackendNoAgent, "just a shell")

	_, err := controller.RegisterConversation(context.Background(), session.ID)
	if err == nil || !strings.Contains(err.Error(), "plain shell") {
		t.Fatalf("error = %v", err)
	}
}

// Resume is the reason the registration exists. It must carry the conversation
// to the runtime boundary, and the new session must record that it was handed
// that conversation rather than given a fresh one.
func TestResumeStartsANewSessionCarryingTheOriginalConversation(t *testing.T) {
	controller, supervisor, repository := conversationController(t, ConversationResolver(nil))
	original := mustStart(t, controller, heikou.BackendClaude, "start the work")

	resumed, err := controller.ResumeSession(context.Background(), original.ID, "carry on")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ID == original.ID {
		t.Fatal("resume reused the original durable id instead of starting a new session")
	}
	if got := supervisor.lastRequest(t).Resume; got != original.ID {
		t.Fatalf("runtime resume target = %q, want the original conversation %q", got, original.ID)
	}

	state, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	record, ok := state.Session(resumed.ID)
	if !ok {
		t.Fatal("the resumed session was not recorded")
	}
	if record.Conversation == nil || record.Conversation.ID != original.ID ||
		record.Conversation.Source != workstream.ConversationAssigned {
		t.Fatalf("resumed conversation = %#v", record.Conversation)
	}

	// The original record is the account of what already happened. A resume is
	// new work, so it must not rewrite that account.
	before, ok := state.Session(original.ID)
	if !ok {
		t.Fatal("the original session disappeared")
	}
	if before.Conversation == nil || before.Conversation.ID != original.ID {
		t.Fatalf("resume disturbed the original registration: %#v", before.Conversation)
	}
}

// A Codex resume has to resolve first, and the id it resolves is then passed on
// the command line — so the new session's conversation is assigned even though
// the one it continues was only ever observed.
func TestResumingCodexResolvesFirstThenCarriesTheIdItFound(t *testing.T) {
	resolver := ResolveConversationFunc(func(context.Context, workstream.SessionRecord) (string, error) {
		return "019e6d0c-14bd-7792-91d2-f684a8dc6e80", nil
	})
	controller, supervisor, repository := conversationController(t, resolver)
	original := mustStart(t, controller, heikou.BackendCodex, "start the work")

	resumed, err := controller.ResumeSession(context.Background(), original.ID, "carry on")
	if err != nil {
		t.Fatal(err)
	}
	if got := supervisor.lastRequest(t).Resume; got != "019e6d0c-14bd-7792-91d2-f684a8dc6e80" {
		t.Fatalf("runtime resume target = %q", got)
	}

	state, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The session that was resumed keeps the observed provenance it earned.
	before, _ := state.Session(original.ID)
	if before.Conversation == nil || before.Conversation.Source != workstream.ConversationObserved {
		t.Fatalf("original conversation = %#v", before.Conversation)
	}
	// The new one was handed the id on the command line, so it is assigned.
	after, _ := state.Session(resumed.ID)
	if after.Conversation == nil || after.Conversation.Source != workstream.ConversationAssigned ||
		after.Conversation.ID != "019e6d0c-14bd-7792-91d2-f684a8dc6e80" {
		t.Fatalf("resumed conversation = %#v", after.Conversation)
	}
}

func TestResumeRefusesWhenTheConversationCannotBeEstablished(t *testing.T) {
	resolver := ResolveConversationFunc(func(context.Context, workstream.SessionRecord) (string, error) {
		return "", errors.New("no rollout matches this launch")
	})
	controller, supervisor, _ := conversationController(t, resolver)
	original := mustStart(t, controller, heikou.BackendCodex, "start the work")
	started := len(supervisor.requests)

	if _, err := controller.ResumeSession(context.Background(), original.ID, "carry on"); err == nil {
		t.Fatal("resume proceeded without a conversation")
	}
	if len(supervisor.requests) != started {
		t.Fatal("resume started a session despite having no conversation to continue")
	}
}

func TestResumeRequiresADurableSessionAndAPrompt(t *testing.T) {
	controller, _, _ := conversationController(t, ConversationResolver(nil))
	session := mustStart(t, controller, heikou.BackendClaude, "start the work")

	if _, err := controller.ResumeSession(context.Background(), session.ID, "  "); err == nil {
		t.Fatal("an empty resume prompt was accepted")
	}
	if _, err := controller.ResumeSession(context.Background(), "11111111-2222-4333-8444-555555555555", "go"); err == nil {
		t.Fatal("resuming an unknown session was accepted")
	}
}

// Everything that reads a runner-written file asks a session which id it was
// filed under, so the fallback rule lives in one place rather than at each
// reader. A resumed session is the case that made this matter: its durable id
// names no file and never will.
func TestASessionNamesTheConversationItsTranscriptIsFiledUnder(t *testing.T) {
	const durable = "018f0000-0000-4000-8000-00000000e001"
	const conversation = "018f0000-0000-4000-8000-00000000e002"
	at := time.Now()

	for name, test := range map[string]struct {
		record workstream.SessionRecord
		want   string
	}{
		"a resumed session answers with the conversation it continued": {
			record: workstream.SessionRecord{ID: durable, Conversation: &workstream.Conversation{
				ID: conversation, Source: workstream.ConversationAssigned, RecordedAt: at,
			}},
			want: conversation,
		},
		// An observed id is the one Heikou matched against a file the runner
		// wrote, which makes it the better answer to "which file", not a worse
		// one. Source separates what Heikou caused from what it inferred; it
		// does not rank ids by how well they name a path.
		"an observed registration is used just the same": {
			record: workstream.SessionRecord{ID: durable, Conversation: &workstream.Conversation{
				ID: conversation, Source: workstream.ConversationObserved, RecordedAt: at,
			}},
			want: conversation,
		},
		"a fresh claude session answers with its own id": {
			record: workstream.SessionRecord{ID: durable, Conversation: &workstream.Conversation{
				ID: durable, Source: workstream.ConversationAssigned, RecordedAt: at,
			}},
			want: durable,
		},
		"an unregistered session falls back to the durable id": {
			record: workstream.SessionRecord{ID: durable},
			want:   durable,
		},
		"a blank registration is not allowed to erase the id": {
			record: workstream.SessionRecord{ID: durable, Conversation: &workstream.Conversation{ID: "  "}},
			want:   durable,
		},
	} {
		t.Run(name, func(t *testing.T) {
			session := Session{ID: durable, Backend: heikou.BackendClaude, Record: test.record}
			if got := session.ConversationID(); got != test.want {
				t.Fatalf("ConversationID() = %q, want %q", got, test.want)
			}
		})
	}

	// An orphan has a runtime and no durable record at all, so the only id it
	// has is the one the pane carries.
	orphan := Session{ID: durable, Orphaned: true}
	if got := orphan.ConversationID(); got != durable {
		t.Fatalf("orphan ConversationID() = %q, want %q", got, durable)
	}
}
