package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sync"
	"time"
)

type State string

const (
	StatePending          State = "pending"
	StateConfiguring      State = "configuring"
	StateFinalizing       State = "finalizing"
	StateInstalled        State = "installed"
	StateRecoveryRequired State = "recovery_required"
)

var (
	ErrStateConflict  = errors.New("setup state does not permit this operation")
	ErrInvalidState   = errors.New("setup state file is invalid")
	ErrAlreadyExists  = errors.New("setup state already exists")
	ErrRecoveryNeeded = errors.New("setup recovery is required")
)

type StateRecord struct {
	Version   int       `json:"version"`
	Status    State     `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

type StateStore struct {
	files SecureFiles
	path  string
	mu    sync.Mutex
}

func NewStateStore(files SecureFiles, path string) (*StateStore, error) {
	if files == nil {
		return nil, errors.New("setup state file store is required")
	}
	if path == "" {
		return nil, errors.New("setup state path is required")
	}
	return &StateStore{files: files, path: path}, nil
}

func (store *StateStore) Load() (StateRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.loadLocked()
}

func (store *StateStore) Initialize(now time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	record := StateRecord{Version: 1, Status: StatePending, UpdatedAt: now.UTC()}
	contents, err := marshalState(record)
	if err != nil {
		return err
	}
	if err := store.files.CreateAtomic(store.path, contents); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("create setup state: %w", err)
	}
	return nil
}

func (store *StateStore) Transition(from, to State, now time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, err := store.loadLocked()
	if err != nil {
		return err
	}
	if record.Status != from || !allowedTransition(from, to) {
		return ErrStateConflict
	}
	record.Status = to
	record.UpdatedAt = now.UTC()
	contents, err := marshalState(record)
	if err != nil {
		return err
	}
	if err := store.files.WriteAtomic(store.path, contents); err != nil {
		return fmt.Errorf("persist setup state transition: %w", err)
	}
	return nil
}

func (store *StateStore) MarkRecovery(now time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, err := store.loadLocked()
	if err != nil {
		return err
	}
	if record.Status == StateRecoveryRequired {
		return nil
	}
	if !allowedTransition(record.Status, StateRecoveryRequired) {
		return ErrStateConflict
	}
	record.Status = StateRecoveryRequired
	record.UpdatedAt = now.UTC()
	contents, err := marshalState(record)
	if err != nil {
		return err
	}
	if err := store.files.WriteAtomic(store.path, contents); err != nil {
		return fmt.Errorf("persist setup recovery state: %w", err)
	}
	return nil
}

func (store *StateStore) loadLocked() (StateRecord, error) {
	contents, err := store.files.Read(store.path, 8*1024)
	if err != nil {
		return StateRecord{}, fmt.Errorf("read setup state: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var record StateRecord
	if err := decoder.Decode(&record); err != nil {
		return StateRecord{}, ErrInvalidState
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return StateRecord{}, ErrInvalidState
	}
	if record.Version != 1 || !validState(record.Status) || record.UpdatedAt.IsZero() || record.UpdatedAt.Location() != time.UTC {
		return StateRecord{}, ErrInvalidState
	}
	return record, nil
}

func marshalState(record StateRecord) ([]byte, error) {
	contents, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode setup state: %w", err)
	}
	return append(contents, '\n'), nil
}

func validState(state State) bool {
	switch state {
	case StatePending, StateConfiguring, StateFinalizing, StateInstalled, StateRecoveryRequired:
		return true
	default:
		return false
	}
}

func allowedTransition(from, to State) bool {
	if to == StateRecoveryRequired {
		return from == StatePending || from == StateConfiguring || from == StateFinalizing
	}
	switch from {
	case StatePending:
		return to == StateConfiguring
	case StateConfiguring:
		return to == StateFinalizing
	case StateFinalizing:
		return to == StateInstalled
	default:
		return false
	}
}
