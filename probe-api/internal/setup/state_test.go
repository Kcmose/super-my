package setup

import (
	"errors"
	"testing"
	"time"
)

func TestStateMachineHappyPathAndTerminalInstalled(t *testing.T) {
	files := newMemoryFiles()
	store, err := NewStateStore(files, "/state.json")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	if err := store.Initialize(now); err != nil {
		t.Fatal(err)
	}
	transitions := []struct{ from, to State }{
		{StatePending, StateConfiguring},
		{StateConfiguring, StateFinalizing},
		{StateFinalizing, StateInstalled},
	}
	for index, transition := range transitions {
		if err := store.Transition(transition.from, transition.to, now.Add(time.Duration(index+1)*time.Second)); err != nil {
			t.Fatalf("transition %s -> %s: %v", transition.from, transition.to, err)
		}
	}
	record, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != StateInstalled {
		t.Fatalf("status = %q, want installed", record.Status)
	}
	if err := store.Transition(StateInstalled, StatePending, now); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("terminal transition error = %v", err)
	}
	if err := store.MarkRecovery(now); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("installed recovery error = %v", err)
	}
}

func TestStateMachineRejectsSkipsAndCanFailClosed(t *testing.T) {
	files := newMemoryFiles()
	store, _ := NewStateStore(files, "/state.json")
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	if err := store.Initialize(now); err != nil {
		t.Fatal(err)
	}
	if err := store.Initialize(now); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate init error = %v", err)
	}
	if err := store.Transition(StatePending, StateInstalled, now); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("skipped transition error = %v", err)
	}
	if err := store.MarkRecovery(now); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRecovery(now); err != nil {
		t.Fatalf("repeated recovery must be idempotent: %v", err)
	}
	record, _ := store.Load()
	if record.Status != StateRecoveryRequired {
		t.Fatalf("status = %q", record.Status)
	}
}

func TestStateStoreRejectsMalformedPersistentState(t *testing.T) {
	for name, contents := range map[string]string{
		"unknown state": `{"version":1,"status":"other","updated_at":"2026-08-24T10:00:00Z"}`,
		"unknown field": `{"version":1,"status":"pending","updated_at":"2026-08-24T10:00:00Z","extra":true}`,
		"trailing data": `{"version":1,"status":"pending","updated_at":"2026-08-24T10:00:00Z"}{}`,
		"wrong version": `{"version":2,"status":"pending","updated_at":"2026-08-24T10:00:00Z"}`,
	} {
		t.Run(name, func(t *testing.T) {
			files := newMemoryFiles()
			files.files["/state.json"] = []byte(contents)
			store, _ := NewStateStore(files, "/state.json")
			if _, err := store.Load(); !errors.Is(err, ErrInvalidState) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
