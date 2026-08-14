package workstream

import (
	"crypto/rand"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zamborg/heikou/internal/heikou"
)

const (
	StateVersion         = 3
	MaxSessionTitleRunes = 120
	// MaxConversationIDRunes bounds a runner-owned identifier. Both runners mint
	// UUIDs today, so this is far above what either produces; it exists because
	// the value is another program's to choose and durable state should not grow
	// without limit on that program's say-so.
	MaxConversationIDRunes = 200
)

type LaunchStatus string

const (
	LaunchPending LaunchStatus = "pending"
	LaunchStarted LaunchStatus = "started"
)

type OutcomeKind string

const (
	OutcomeExited      OutcomeKind = "exited"
	OutcomeStopped     OutcomeKind = "stopped"
	OutcomeStartFailed OutcomeKind = "start_failed"
)

// Workstream is durable organization. It deliberately has no execution or
// coordination semantics.
type Workstream struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	ArtifactDir string     `json:"artifact_dir"`
	Roots       []string   `json:"roots"`
	Revision    uint64     `json:"revision"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
}

// RuntimeBinding identifies where a launch was placed. It is a durable
// locator, not a claim about whether the process is currently alive.
type RuntimeBinding struct {
	Driver      string    `json:"driver"`
	Socket      string    `json:"socket"`
	SessionName string    `json:"session_name"`
	BoundAt     time.Time `json:"bound_at"`
}

type LaunchIntent struct {
	Status  LaunchStatus    `json:"status"`
	Binding *RuntimeBinding `json:"binding,omitempty"`
}

type Outcome struct {
	Kind       OutcomeKind `json:"kind"`
	ExitCode   *int        `json:"exit_code,omitempty"`
	Error      string      `json:"error,omitempty"`
	RecordedAt time.Time   `json:"recorded_at"`
}

// ConversationSource records how Heikou came to know a native conversation id.
// The distinction is the whole point of storing it: one of these is a fact
// Heikou caused, and the other is a match Heikou made against files another
// program wrote. Collapsing them would let an inference be read as a guarantee.
type ConversationSource string

const (
	// ConversationAssigned means Heikou chose the id and handed it to the runner
	// on the command line. The runner had no say, so the id is certain.
	ConversationAssigned ConversationSource = "assigned"
	// ConversationObserved means the runner minted its own id and Heikou matched
	// a runner-written record back to this launch afterwards. It is evidence,
	// not a guarantee: see internal/transcript for exactly what must agree
	// before a match is accepted, and what makes Heikou refuse to record one.
	ConversationObserved ConversationSource = "observed"
)

// Conversation is the native runner's own identity for what was said in a
// session. It is what survives the pane: a tmux runtime is gone the moment it
// dies, but the runner's conversation is on disk and can be resumed by id.
//
// It is deliberately separate from SessionRecord.ID even though the two are
// equal for a Claude session Heikou launched fresh. That equality is a property
// of today's Claude adapter, not of the model — a resumed session carries the
// conversation of the session it continued, and its own durable id is new.
type Conversation struct {
	ID     string             `json:"id"`
	Source ConversationSource `json:"source"`
	// RecordedAt is when Heikou registered the id, not when the conversation
	// began. For an observed id those differ by however long it took to ask.
	RecordedAt time.Time `json:"recorded_at"`
}

// SessionRecord owns launch identity, optional user-authored display identity,
// and durable outcome. Live process observations remain exclusively in the
// Supervisor projection.
type SessionRecord struct {
	ID      string         `json:"id"`
	Backend heikou.Backend `json:"backend"`
	// Title never renames the tmux runtime or native provider conversation.
	Title         string       `json:"title,omitempty"`
	InitialPrompt string       `json:"initial_prompt"`
	InitialRoot   string       `json:"initial_root"`
	CreatedAt     time.Time    `json:"created_at"`
	Launch        LaunchIntent `json:"launch"`
	Outcome       *Outcome     `json:"outcome,omitempty"`
	// Conversation is the runner's identity for this session's conversation,
	// absent until Heikou can state it without guessing.
	Conversation *Conversation `json:"conversation,omitempty"`
}

type Membership struct {
	WorkstreamID string    `json:"workstream_id"`
	SessionID    string    `json:"session_id"`
	JoinedAt     time.Time `json:"joined_at"`
}

type State struct {
	Version     int             `json:"version"`
	Revision    uint64          `json:"revision"`
	Workstreams []Workstream    `json:"workstreams"`
	Sessions    []SessionRecord `json:"sessions"`
	Memberships []Membership    `json:"memberships"`
}

func EmptyState() State {
	return State{Version: StateVersion}
}

func NewID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate durable id: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func (s State) Workstream(id string) (Workstream, bool) {
	for _, candidate := range s.Workstreams {
		if candidate.ID == id {
			return candidate, true
		}
	}
	return Workstream{}, false
}

func (s State) Session(id string) (SessionRecord, bool) {
	for _, candidate := range s.Sessions {
		if candidate.ID == id {
			return candidate, true
		}
	}
	return SessionRecord{}, false
}

func (s State) WorkstreamForSession(sessionID string) string {
	for _, membership := range s.Memberships {
		if membership.SessionID == sessionID {
			return membership.WorkstreamID
		}
	}
	return ""
}

func (s State) Validate() error {
	return s.validateVersion(StateVersion)
}

// validateVersion validates the domain invariants shared by persisted schema
// versions. Migration code uses it to prove an older state is valid before
// applying the next ordered schema transition.
func (s State) validateVersion(version int) error {
	if s.Version != version {
		return fmt.Errorf("state version %d does not match schema version %d", s.Version, version)
	}
	workstreams := make(map[string]Workstream, len(s.Workstreams))
	for _, item := range s.Workstreams {
		if !ValidID(item.ID) {
			return fmt.Errorf("workstream %q has an invalid id", item.ID)
		}
		if _, exists := workstreams[item.ID]; exists {
			return fmt.Errorf("duplicate workstream id %q", item.ID)
		}
		if strings.TrimSpace(item.Name) == "" {
			return fmt.Errorf("workstream %s has an empty name", item.ID)
		}
		if !filepath.IsAbs(item.ArtifactDir) {
			return fmt.Errorf("workstream %s artifact_dir must be absolute", item.ID)
		}
		if len(item.Roots) == 0 {
			return fmt.Errorf("workstream %s must have at least one root", item.ID)
		}
		seenRoots := make(map[string]struct{}, len(item.Roots))
		for _, root := range item.Roots {
			if !filepath.IsAbs(root) {
				return fmt.Errorf("workstream %s root %q must be absolute", item.ID, root)
			}
			clean := filepath.Clean(root)
			if _, exists := seenRoots[clean]; exists {
				return fmt.Errorf("workstream %s repeats root %q", item.ID, clean)
			}
			seenRoots[clean] = struct{}{}
		}
		if item.Revision == 0 || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
			return fmt.Errorf("workstream %s is missing revision timestamps", item.ID)
		}
		workstreams[item.ID] = item
	}

	sessions := make(map[string]struct{}, len(s.Sessions))
	for _, item := range s.Sessions {
		if !ValidID(item.ID) {
			return fmt.Errorf("session %q has an invalid id", item.ID)
		}
		if _, exists := sessions[item.ID]; exists {
			return fmt.Errorf("duplicate session id %q", item.ID)
		}
		if _, err := heikou.ParseBackend(string(item.Backend)); err != nil {
			return fmt.Errorf("session %s: %w", item.ID, err)
		}
		if strings.TrimSpace(item.InitialPrompt) == "" {
			return fmt.Errorf("session %s has an empty initial prompt", item.ID)
		}
		if version >= 2 {
			if err := validateSessionTitle(item.Title); err != nil {
				return fmt.Errorf("session %s has an invalid title: %w", item.ID, err)
			}
		}
		if version >= 3 {
			if err := validateConversation(item.Conversation); err != nil {
				return fmt.Errorf("session %s has an invalid conversation: %w", item.ID, err)
			}
		}
		if !filepath.IsAbs(item.InitialRoot) || item.CreatedAt.IsZero() {
			return fmt.Errorf("session %s has invalid launch metadata", item.ID)
		}
		if item.Launch.Status != LaunchPending && item.Launch.Status != LaunchStarted {
			return fmt.Errorf("session %s has invalid launch status %q", item.ID, item.Launch.Status)
		}
		if item.Launch.Status == LaunchStarted && item.Launch.Binding == nil {
			return fmt.Errorf("session %s is started without a runtime binding", item.ID)
		}
		if item.Launch.Binding != nil {
			binding := item.Launch.Binding
			if binding.Driver != "tmux" || binding.Socket == "" || binding.SessionName == "" || binding.BoundAt.IsZero() {
				return fmt.Errorf("session %s has an invalid runtime binding", item.ID)
			}
		}
		if item.Outcome != nil {
			switch item.Outcome.Kind {
			case OutcomeExited:
				if item.Outcome.ExitCode == nil {
					return fmt.Errorf("session %s exited without an exit code", item.ID)
				}
			case OutcomeStopped:
			case OutcomeStartFailed:
				if strings.TrimSpace(item.Outcome.Error) == "" {
					return fmt.Errorf("session %s start_failed without an error", item.ID)
				}
			default:
				return fmt.Errorf("session %s has invalid outcome %q", item.ID, item.Outcome.Kind)
			}
			if item.Outcome.RecordedAt.IsZero() {
				return fmt.Errorf("session %s outcome is missing recorded_at", item.ID)
			}
		}
		sessions[item.ID] = struct{}{}
	}

	memberships := make(map[string]struct{}, len(s.Memberships))
	for _, item := range s.Memberships {
		workstream, ok := workstreams[item.WorkstreamID]
		if !ok {
			return fmt.Errorf("membership references missing workstream %q", item.WorkstreamID)
		}
		if workstream.ArchivedAt != nil {
			return fmt.Errorf("membership references archived workstream %q", item.WorkstreamID)
		}
		if _, ok := sessions[item.SessionID]; !ok {
			return fmt.Errorf("membership references missing session %q", item.SessionID)
		}
		if _, exists := memberships[item.SessionID]; exists {
			return fmt.Errorf("session %s belongs to more than one workstream", item.SessionID)
		}
		if item.JoinedAt.IsZero() {
			return fmt.Errorf("membership for session %s is missing joined_at", item.SessionID)
		}
		memberships[item.SessionID] = struct{}{}
	}
	return nil
}

func validateSessionTitle(title string) error {
	if title == "" {
		return nil
	}
	if !utf8.ValidString(title) {
		return errors.New("must be valid UTF-8")
	}
	if strings.TrimSpace(title) != title {
		return errors.New("must not have leading or trailing whitespace")
	}
	if utf8.RuneCountInString(title) > MaxSessionTitleRunes {
		return fmt.Errorf("must be at most %d characters", MaxSessionTitleRunes)
	}
	for _, r := range title {
		if unicode.IsControl(r) {
			return errors.New("must not contain control characters")
		}
	}
	return nil
}

// validateConversation refuses a registration that cannot be acted on. An
// absent conversation is the normal state for a session Heikou has not been
// able to name yet, so nil is valid; a present one must be usable as a runner
// argument, which is why whitespace and control characters are rejected rather
// than trimmed.
func validateConversation(conversation *Conversation) error {
	if conversation == nil {
		return nil
	}
	id := conversation.ID
	if id == "" {
		return errors.New("id cannot be empty")
	}
	if !utf8.ValidString(id) {
		return errors.New("id must be valid UTF-8")
	}
	if strings.TrimSpace(id) != id {
		return errors.New("id must not have leading or trailing whitespace")
	}
	if utf8.RuneCountInString(id) > MaxConversationIDRunes {
		return fmt.Errorf("id must be at most %d characters", MaxConversationIDRunes)
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return errors.New("id must not contain control characters")
		}
	}
	switch conversation.Source {
	case ConversationAssigned, ConversationObserved:
	default:
		return fmt.Errorf("unknown source %q (want assigned or observed)", conversation.Source)
	}
	if conversation.RecordedAt.IsZero() {
		return errors.New("missing recorded_at")
	}
	return nil
}

func ValidID(value string) bool {
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

func NormalizeRoot(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("root cannot be empty")
	}
	root, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	return filepath.Clean(root), nil
}
