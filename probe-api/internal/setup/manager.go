package setup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	defaultSessionTTL = 15 * time.Minute
	maximumSessionTTL = 30 * time.Minute
)

var (
	ErrInvalidSession      = errors.New("setup session is invalid or expired")
	ErrInvalidCSRF         = errors.New("setup CSRF token is invalid")
	ErrFinalizerRequired   = errors.New("setup finalizer is unavailable")
	ErrFinalizationPending = errors.New("privileged setup finalizer outcome is still pending")
)

type memorySession struct {
	tokenHash [sha256.Size]byte
	csrfHash  [sha256.Size]byte
	expiresAt time.Time
}

type SessionCredentials struct {
	SessionToken string    `json:"session_token"`
	CSRFToken    string    `json:"csrf_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type Finalizer interface {
	Finalize(context.Context, CompleteRequest) error
}

type FinalizerFunc func(context.Context, CompleteRequest) error

func (function FinalizerFunc) Finalize(ctx context.Context, request CompleteRequest) error {
	return function(ctx, request)
}

type Manager struct {
	states       *StateStore
	now          func() time.Time
	random       io.Reader
	sessionTTL   time.Duration
	mu           sync.Mutex
	sessions     []memorySession
	finalization bool
}

type ManagerOption func(*Manager)

func WithClock(clock func() time.Time) ManagerOption {
	return func(manager *Manager) { manager.now = clock }
}

func WithRandom(reader io.Reader) ManagerOption {
	return func(manager *Manager) { manager.random = reader }
}

func WithSessionTTL(duration time.Duration) ManagerOption {
	return func(manager *Manager) { manager.sessionTTL = duration }
}

func NewManager(states *StateStore, options ...ManagerOption) (*Manager, error) {
	if states == nil {
		return nil, errors.New("setup state store is required")
	}
	manager := &Manager{
		states: states, now: time.Now, random: rand.Reader, sessionTTL: defaultSessionTTL,
	}
	for _, option := range options {
		option(manager)
	}
	if manager.now == nil || manager.random == nil || manager.sessionTTL <= 0 || manager.sessionTTL > maximumSessionTTL {
		return nil, errors.New("setup manager options are invalid")
	}
	return manager, nil
}

// Initialize creates only the persistent fail-closed state. Authentication for
// the temporary setup UI is provided by the root-only Unix socket, so no
// plaintext setup code or code-derived record exists.
func (manager *Manager) Initialize() error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.states.Initialize(manager.now().UTC())
}

func (manager *Manager) Status() (State, error) {
	record, err := manager.states.Load()
	if err != nil {
		return "", err
	}
	return record.Status, nil
}

func (manager *Manager) ReconcileOnStart() error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	record, err := manager.states.Load()
	if err != nil {
		return err
	}
	manager.sessions = nil
	manager.finalization = false
	switch record.Status {
	case StatePending, StateConfiguring:
		// No privileged side effect has started. A root Unix-socket client may
		// establish a fresh in-memory session after a service restart.
		return nil
	case StateFinalizing:
		// The privileged finalizer can outlive an HTTP setup-process restart.
		// Its root-owned state commit or explicit failure path determines the
		// terminal state; this process must not guess that it has stopped.
		return nil
	case StateInstalled:
		// Serve the short installed handoff window and then exit normally.
		return nil
	case StateRecoveryRequired:
		return ErrRecoveryNeeded
	default:
		return ErrInvalidState
	}
}

// CreateSession creates or rotates the sole in-memory setup session. The first
// successful call advances pending to configuring; later calls while
// configuring deliberately rotate both credentials so a refresh or a setup
// service restart can recover without a user-visible installation code.
func (manager *Manager) CreateSession() (SessionCredentials, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	now := manager.now().UTC()
	state, err := manager.states.Load()
	if err != nil {
		return SessionCredentials{}, err
	}
	if state.Status != StatePending && state.Status != StateConfiguring {
		return SessionCredentials{}, ErrStateConflict
	}

	sessionToken, sessionHash, err := manager.randomToken()
	if err != nil {
		return SessionCredentials{}, err
	}
	csrfToken, csrfHash, err := manager.randomToken()
	if err != nil {
		return SessionCredentials{}, err
	}

	if state.Status == StatePending {
		if err := manager.states.Transition(StatePending, StateConfiguring, now); err != nil {
			return SessionCredentials{}, err
		}
	}
	expiresAt := now.Add(manager.sessionTTL)
	manager.sessions = []memorySession{{tokenHash: sessionHash, csrfHash: csrfHash, expiresAt: expiresAt}}
	return SessionCredentials{SessionToken: sessionToken, CSRFToken: csrfToken, ExpiresAt: expiresAt}, nil
}

func (manager *Manager) BeginFinalization(sessionToken, csrfToken string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.finalization {
		return ErrStateConflict
	}
	now := manager.now().UTC()
	manager.removeExpiredSessions(now)
	tokenHash := sha256.Sum256([]byte(sessionToken))
	csrfHash := sha256.Sum256([]byte(csrfToken))
	matched := -1
	csrfMatches := false
	for index := range manager.sessions {
		if subtle.ConstantTimeCompare(manager.sessions[index].tokenHash[:], tokenHash[:]) == 1 {
			matched = index
			csrfMatches = subtle.ConstantTimeCompare(manager.sessions[index].csrfHash[:], csrfHash[:]) == 1
		}
	}
	if matched < 0 {
		return ErrInvalidSession
	}
	if !csrfMatches {
		return ErrInvalidCSRF
	}
	if err := manager.states.Transition(StateConfiguring, StateFinalizing, now); err != nil {
		return err
	}
	manager.sessions = nil
	manager.finalization = true
	return nil
}

func (manager *Manager) FinishFinalization(success bool) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.finalization = false
	record, err := manager.states.Load()
	if err != nil {
		return err
	}
	if record.Status == StateInstalled {
		// Persistent state is authoritative even when the broker lost or could
		// not decode the final result after the privileged commit succeeded.
		return nil
	}
	if record.Status == StateRecoveryRequired {
		// The privileged worker's durable recovery decision wins over any
		// stale or malformed broker result.
		return nil
	}
	if record.Status == StateConfiguring {
		// The root worker may make this one reverse transition only when its
		// failure was proven to be preflight-only. A claimed success without an
		// installed commit must still fail closed.
		if success {
			return ErrStateConflict
		}
		return nil
	}
	if record.Status != StateFinalizing {
		return ErrStateConflict
	}
	// A broker timeout/cancellation can race a still-running root worker. Only
	// that worker may commit installed or recovery_required, so preserve the
	// in-flight state and let status polling observe its eventual decision.
	return ErrFinalizationPending
}

func (manager *Manager) randomToken() (string, [sha256.Size]byte, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(manager.random, raw); err != nil {
		return "", [sha256.Size]byte{}, errors.New("generate setup session token")
	}
	token := hex.EncodeToString(raw)
	clear(raw)
	return token, sha256.Sum256([]byte(token)), nil
}

func (manager *Manager) removeExpiredSessions(now time.Time) {
	retained := manager.sessions[:0]
	for _, session := range manager.sessions {
		if now.Before(session.expiresAt) {
			retained = append(retained, session)
		}
	}
	manager.sessions = retained
}
