package setup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync"
	"time"
)

const (
	defaultCodeTTL    = 30 * time.Minute
	defaultSessionTTL = 15 * time.Minute
)

var (
	ErrInvalidCode       = errors.New("setup code is invalid or expired")
	ErrInvalidSession    = errors.New("setup session is invalid or expired")
	ErrInvalidCSRF       = errors.New("setup CSRF token is invalid")
	ErrFinalizerRequired = errors.New("setup finalizer is unavailable")
)

type codeRecord struct {
	CodeSHA256 string     `json:"code_sha256"`
	ExpiresAt  time.Time  `json:"expires_at"`
	ConsumedAt *time.Time `json:"consumed_at,omitempty"`
}

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
	files        SecureFiles
	codePath     string
	now          func() time.Time
	random       io.Reader
	codeTTL      time.Duration
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

func NewManager(states *StateStore, files SecureFiles, codePath string, options ...ManagerOption) (*Manager, error) {
	if states == nil || files == nil || codePath == "" {
		return nil, errors.New("setup state, file store, and code path are required")
	}
	manager := &Manager{
		states: states, files: files, codePath: codePath,
		now: time.Now, random: rand.Reader, codeTTL: defaultCodeTTL, sessionTTL: defaultSessionTTL,
	}
	for _, option := range options {
		option(manager)
	}
	if manager.now == nil || manager.random == nil || manager.sessionTTL <= 0 || manager.sessionTTL > defaultCodeTTL {
		return nil, errors.New("setup manager options are invalid")
	}
	return manager, nil
}

// Initialize creates a fresh fail-closed setup state and returns the only
// plaintext copy of the 256-bit setup code. Repeated calls never rotate or
// reopen an existing installation.
func (manager *Manager) Initialize() (string, time.Time, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	now := manager.now().UTC()
	raw := make([]byte, 32)
	if _, err := io.ReadFull(manager.random, raw); err != nil {
		return "", time.Time{}, errors.New("generate setup code")
	}
	code := hex.EncodeToString(raw)
	digest := sha256.Sum256([]byte(code))
	clear(raw)
	expiresAt := now.Add(manager.codeTTL)
	record := codeRecord{CodeSHA256: hex.EncodeToString(digest[:]), ExpiresAt: expiresAt}
	contents, err := marshalCodeRecord(record)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := manager.states.Initialize(now); err != nil {
		return "", time.Time{}, err
	}
	if err := manager.files.CreateAtomic(manager.codePath, contents); err != nil {
		_ = manager.states.MarkRecovery(now)
		if errors.Is(err, fs.ErrExist) {
			return "", time.Time{}, ErrAlreadyExists
		}
		return "", time.Time{}, fmt.Errorf("create setup code record: %w", err)
	}
	return code, expiresAt, nil
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
	switch record.Status {
	case StatePending:
		code, err := manager.loadCode()
		if err != nil || code.ConsumedAt != nil || !manager.now().Before(code.ExpiresAt) {
			_ = manager.states.MarkRecovery(manager.now())
			return ErrRecoveryNeeded
		}
		return nil
	case StateConfiguring, StateFinalizing:
		_ = manager.states.MarkRecovery(manager.now())
		return ErrRecoveryNeeded
	case StateInstalled:
		return ErrStateConflict
	case StateRecoveryRequired:
		return ErrRecoveryNeeded
	default:
		return ErrInvalidState
	}
}

func (manager *Manager) ExchangeCode(candidate string) (SessionCredentials, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	now := manager.now().UTC()
	state, err := manager.states.Load()
	if err != nil || state.Status != StatePending {
		return SessionCredentials{}, ErrStateConflict
	}
	record, err := manager.loadCode()
	if err != nil {
		_ = manager.states.MarkRecovery(now)
		return SessionCredentials{}, ErrRecoveryNeeded
	}
	expected, err := hex.DecodeString(record.CodeSHA256)
	if err != nil || len(expected) != sha256.Size {
		_ = manager.states.MarkRecovery(now)
		return SessionCredentials{}, ErrRecoveryNeeded
	}
	candidateHash := sha256.Sum256([]byte(candidate))
	matches := subtle.ConstantTimeCompare(expected, candidateHash[:]) == 1
	clear(expected)
	if !matches || record.ConsumedAt != nil || !now.Before(record.ExpiresAt) {
		return SessionCredentials{}, ErrInvalidCode
	}
	sessionToken, sessionHash, err := manager.randomToken()
	if err != nil {
		return SessionCredentials{}, err
	}
	csrfToken, csrfHash, err := manager.randomToken()
	if err != nil {
		return SessionCredentials{}, err
	}
	record.ConsumedAt = &now
	contents, err := marshalCodeRecord(record)
	if err != nil {
		return SessionCredentials{}, err
	}
	if err := manager.files.WriteAtomic(manager.codePath, contents); err != nil {
		_ = manager.states.MarkRecovery(now)
		return SessionCredentials{}, ErrRecoveryNeeded
	}
	if err := manager.states.Transition(StatePending, StateConfiguring, now); err != nil {
		_ = manager.states.MarkRecovery(now)
		return SessionCredentials{}, ErrRecoveryNeeded
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
	now := manager.now().UTC()
	if !success {
		return manager.states.MarkRecovery(now)
	}
	if err := manager.files.Remove(manager.codePath); err != nil && !errors.Is(err, ErrFileNotFound) {
		_ = manager.states.MarkRecovery(now)
		return fmt.Errorf("destroy setup code record: %w", err)
	}
	if err := manager.states.Transition(StateFinalizing, StateInstalled, now); err != nil {
		_ = manager.states.MarkRecovery(now)
		return err
	}
	return nil
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

func (manager *Manager) loadCode() (codeRecord, error) {
	contents, err := manager.files.Read(manager.codePath, 8*1024)
	if err != nil {
		return codeRecord{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var record codeRecord
	if err := decoder.Decode(&record); err != nil {
		return codeRecord{}, errors.New("setup code record is invalid")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return codeRecord{}, errors.New("setup code record is invalid")
	}
	if len(record.CodeSHA256) != 64 || strings.Trim(record.CodeSHA256, "0123456789abcdef") != "" || record.ExpiresAt.IsZero() || record.ExpiresAt.Location() != time.UTC {
		return codeRecord{}, errors.New("setup code record is invalid")
	}
	if record.ConsumedAt != nil && (record.ConsumedAt.IsZero() || record.ConsumedAt.Location() != time.UTC || record.ConsumedAt.Before(record.ExpiresAt.Add(-defaultCodeTTL)) || record.ConsumedAt.After(record.ExpiresAt)) {
		return codeRecord{}, errors.New("setup code record is invalid")
	}
	return record, nil
}

func marshalCodeRecord(record codeRecord) ([]byte, error) {
	contents, err := json.Marshal(record)
	if err != nil {
		return nil, errors.New("encode setup code record")
	}
	return append(contents, '\n'), nil
}
