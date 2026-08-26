package setup

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestManagerInitializesWithoutCodeAndAcceptsPrivilegedInstalledCommit(t *testing.T) {
	files := newMemoryFiles()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	manager := newTestManager(files, func() time.Time { return now }, sessionTestRandom(4))
	if err := manager.Initialize(); err != nil {
		t.Fatal(err)
	}
	if len(files.files) != 1 {
		t.Fatalf("persistent setup files = %#v, want only state", files.files)
	}
	if _, exists := files.files["/state.json"]; !exists {
		t.Fatal("persistent setup state was not created")
	}
	if err := manager.Initialize(); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate initialize error = %v", err)
	}

	credentials, err := manager.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials.SessionToken) != 64 || len(credentials.CSRFToken) != 64 || credentials.ExpiresAt.Sub(now) != 15*time.Minute {
		t.Fatalf("invalid session credentials: %#v", credentials)
	}
	if state, _ := manager.Status(); state != StateConfiguring {
		t.Fatalf("state after first session = %q", state)
	}
	if err := manager.BeginFinalization(credentials.SessionToken, string(bytes.Repeat([]byte{'0'}, 64))); !errors.Is(err, ErrInvalidCSRF) {
		t.Fatalf("bad csrf error = %v", err)
	}
	if err := manager.BeginFinalization(credentials.SessionToken, credentials.CSRFToken); err != nil {
		t.Fatal(err)
	}
	if err := manager.BeginFinalization(credentials.SessionToken, credentials.CSRFToken); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("concurrent finalization error = %v", err)
	}
	if err := manager.states.Transition(StateFinalizing, StateInstalled, now); err != nil {
		t.Fatalf("privileged installed commit: %v", err)
	}
	if err := manager.FinishFinalization(true); err != nil {
		t.Fatal(err)
	}
	if state, _ := manager.Status(); state != StateInstalled {
		t.Fatalf("final state = %q", state)
	}
}

func TestManagerRotatesSessionAndRestartCanResign(t *testing.T) {
	files := newMemoryFiles()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	random := sessionTestRandom(4)
	manager := newTestManager(files, func() time.Time { return now }, random)
	if err := manager.Initialize(); err != nil {
		t.Fatal(err)
	}
	first, err := manager.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionToken == second.SessionToken || first.CSRFToken == second.CSRFToken {
		t.Fatal("session rotation reused credentials")
	}
	if err := manager.BeginFinalization(first.SessionToken, first.CSRFToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("rotated session error = %v", err)
	}

	restarted := newTestManager(files, func() time.Time { return now }, sessionTestRandom(4))
	if err := restarted.ReconcileOnStart(); err != nil {
		t.Fatalf("reconcile configuring state: %v", err)
	}
	if err := restarted.BeginFinalization(second.SessionToken, second.CSRFToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("pre-restart session error = %v", err)
	}
	replacement, err := restarted.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.BeginFinalization(replacement.SessionToken, replacement.CSRFToken); err != nil {
		t.Fatal(err)
	}
}

func TestManagerConcurrentSessionRotationLeavesOneCredentialPair(t *testing.T) {
	files := newMemoryFiles()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	const count = 24
	manager := newTestManager(files, func() time.Time { return now }, sessionTestRandom(count+2))
	if err := manager.Initialize(); err != nil {
		t.Fatal(err)
	}

	credentials := make(chan SessionCredentials, count)
	errorsChannel := make(chan error, count)
	var wait sync.WaitGroup
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			value, err := manager.CreateSession()
			credentials <- value
			errorsChannel <- err
		}()
	}
	wait.Wait()
	close(credentials)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("concurrent CreateSession: %v", err)
		}
	}

	valid := 0
	for value := range credentials {
		err := manager.BeginFinalization(value.SessionToken, value.CSRFToken)
		switch {
		case err == nil:
			valid++
		case errors.Is(err, ErrInvalidSession):
		case errors.Is(err, ErrStateConflict) && valid == 1:
			// The valid pair already moved the manager to finalizing; later
			// attempts are rejected without observing credential validity.
		default:
			t.Fatalf("unexpected credential result: %v", err)
		}
	}
	if valid != 1 {
		t.Fatalf("valid credential pairs = %d, want 1", valid)
	}
}

func TestManagerExpiryFailureAndReconciliationFailClosed(t *testing.T) {
	files := newMemoryFiles()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	manager := newTestManager(files, func() time.Time { return now }, sessionTestRandom(4))
	if err := manager.Initialize(); err != nil {
		t.Fatal(err)
	}
	credentials, err := manager.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(16 * time.Minute)
	if err := manager.BeginFinalization(credentials.SessionToken, credentials.CSRFToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expired session error = %v", err)
	}
	replacement, err := manager.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.BeginFinalization(replacement.SessionToken, replacement.CSRFToken); err != nil {
		t.Fatal(err)
	}
	// The privileged worker, not the HTTP manager, owns the durable failure
	// transition before its result is transported back through the broker.
	if err := manager.states.MarkRecovery(now); err != nil {
		t.Fatal(err)
	}
	if err := manager.FinishFinalization(false); err != nil {
		t.Fatal(err)
	}
	if state, _ := manager.Status(); state != StateRecoveryRequired {
		t.Fatalf("failure state = %q", state)
	}
	if _, err := manager.CreateSession(); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("recovery session error = %v", err)
	}

	files2 := newMemoryFiles()
	now = time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	manager2 := newTestManager(files2, func() time.Time { return now }, sessionTestRandom(4))
	if err := manager2.Initialize(); err != nil {
		t.Fatal(err)
	}
	credentials, _ = manager2.CreateSession()
	if err := manager2.BeginFinalization(credentials.SessionToken, credentials.CSRFToken); err != nil {
		t.Fatal(err)
	}
	restarted := newTestManager(files2, func() time.Time { return now }, sessionTestRandom(4))
	if err := restarted.ReconcileOnStart(); err != nil {
		t.Fatalf("finalizing reconciliation error = %v", err)
	}
	if state, _ := restarted.Status(); state != StateFinalizing {
		t.Fatalf("reconciled state = %q", state)
	}
}

func TestManagerFinalOutcomeUsesPersistentInstalledAsAuthority(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)

	t.Run("broker outcome cannot preempt a running privileged worker", func(t *testing.T) {
		manager := newTestManager(newMemoryFiles(), func() time.Time { return now }, sessionTestRandom(2))
		if err := manager.Initialize(); err != nil {
			t.Fatal(err)
		}
		credentials, err := manager.CreateSession()
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.BeginFinalization(credentials.SessionToken, credentials.CSRFToken); err != nil {
			t.Fatal(err)
		}
		if err := manager.FinishFinalization(true); !errors.Is(err, ErrFinalizationPending) {
			t.Fatalf("uncommitted success error = %v", err)
		}
		if state, _ := manager.Status(); state != StateFinalizing {
			t.Fatalf("uncommitted success state = %q", state)
		}
		if err := manager.FinishFinalization(false); !errors.Is(err, ErrFinalizationPending) {
			t.Fatalf("transport cancellation error = %v", err)
		}
		if state, _ := manager.Status(); state != StateFinalizing {
			t.Fatalf("transport cancellation changed state to %q", state)
		}
	})

	t.Run("lost result cannot reverse privileged commit", func(t *testing.T) {
		manager := newTestManager(newMemoryFiles(), func() time.Time { return now }, sessionTestRandom(2))
		if err := manager.Initialize(); err != nil {
			t.Fatal(err)
		}
		credentials, err := manager.CreateSession()
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.BeginFinalization(credentials.SessionToken, credentials.CSRFToken); err != nil {
			t.Fatal(err)
		}
		if err := manager.states.Transition(StateFinalizing, StateInstalled, now); err != nil {
			t.Fatal(err)
		}
		if err := manager.FinishFinalization(false); err != nil {
			t.Fatalf("lost-result reconciliation error = %v", err)
		}
		if state, _ := manager.Status(); state != StateInstalled {
			t.Fatalf("lost-result state = %q", state)
		}
		if err := manager.ReconcileOnStart(); err != nil {
			t.Fatalf("installed restart reconciliation error = %v", err)
		}
	})

	t.Run("root preflight retry clears the in-memory finalization lock", func(t *testing.T) {
		manager := newTestManager(newMemoryFiles(), func() time.Time { return now }, sessionTestRandom(4))
		if err := manager.Initialize(); err != nil {
			t.Fatal(err)
		}
		credentials, err := manager.CreateSession()
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.BeginFinalization(credentials.SessionToken, credentials.CSRFToken); err != nil {
			t.Fatal(err)
		}
		if err := manager.states.Transition(StateFinalizing, StateConfiguring, now); err != nil {
			t.Fatal(err)
		}
		if err := manager.FinishFinalization(false); err != nil {
			t.Fatalf("retryable broker outcome: %v", err)
		}
		if _, err := manager.CreateSession(); err != nil {
			t.Fatalf("fresh session after preflight failure: %v", err)
		}
	})
}
