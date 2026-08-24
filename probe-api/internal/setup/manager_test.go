package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestManagerInitializeExchangeAndInstall(t *testing.T) {
	files := newMemoryFiles()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	manager := newTestManager(files, clock, bytes.NewReader(bytes.Repeat([]byte{0x41}, 128)))
	code, expiresAt, err := manager.Initialize()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 64 || expiresAt.Sub(now) != 30*time.Minute {
		t.Fatalf("code length/expiry = %d/%s", len(code), expiresAt.Sub(now))
	}
	persisted := string(files.files["/code.json"])
	if strings.Contains(persisted, code) {
		t.Fatal("plaintext setup code was persisted")
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(persisted), &record); err != nil {
		t.Fatal(err)
	}
	if len(record) != 2 || len(record["code_sha256"].(string)) != 64 {
		t.Fatalf("unexpected setup code record: %#v", record)
	}
	if _, _, err := manager.Initialize(); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate initialize error = %v", err)
	}
	if _, err := manager.ExchangeCode(strings.Repeat("0", 64)); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("bad code error = %v", err)
	}
	credentials, err := manager.ExchangeCode(code)
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials.SessionToken) != 64 || len(credentials.CSRFToken) != 64 || credentials.ExpiresAt.Sub(now) != 15*time.Minute {
		t.Fatalf("invalid session credentials: %#v", credentials)
	}
	if _, err := manager.ExchangeCode(code); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("reused code error = %v", err)
	}
	if err := manager.BeginFinalization(credentials.SessionToken, strings.Repeat("0", 64)); !errors.Is(err, ErrInvalidCSRF) {
		t.Fatalf("bad csrf error = %v", err)
	}
	if err := manager.BeginFinalization(credentials.SessionToken, credentials.CSRFToken); err != nil {
		t.Fatal(err)
	}
	if err := manager.BeginFinalization(credentials.SessionToken, credentials.CSRFToken); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("concurrent finalization error = %v", err)
	}
	if err := manager.FinishFinalization(true); err != nil {
		t.Fatal(err)
	}
	status, _ := manager.Status()
	if status != StateInstalled {
		t.Fatalf("status = %q", status)
	}
	if _, exists := files.files["/code.json"]; exists {
		t.Fatal("setup code record remains after installation")
	}
}

func TestManagerSessionExpiresAndFinalizerFailureRequiresRecovery(t *testing.T) {
	files := newMemoryFiles()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	manager := newTestManager(files, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x22}, 96)))
	code, _, _ := manager.Initialize()
	credentials, err := manager.ExchangeCode(code)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(16 * time.Minute)
	if err := manager.BeginFinalization(credentials.SessionToken, credentials.CSRFToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expired session error = %v", err)
	}

	files2 := newMemoryFiles()
	now = time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	manager2 := newTestManager(files2, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x33}, 96)))
	code, _, _ = manager2.Initialize()
	credentials, _ = manager2.ExchangeCode(code)
	if err := manager2.BeginFinalization(credentials.SessionToken, credentials.CSRFToken); err != nil {
		t.Fatal(err)
	}
	if err := manager2.FinishFinalization(false); err != nil {
		t.Fatal(err)
	}
	status, _ := manager2.Status()
	if status != StateRecoveryRequired {
		t.Fatalf("status = %q", status)
	}
}

func TestManagerReconcileFailsClosedAfterRestartOrExpiry(t *testing.T) {
	files := newMemoryFiles()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	manager := newTestManager(files, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x44}, 96)))
	code, _, _ := manager.Initialize()
	if _, err := manager.ExchangeCode(code); err != nil {
		t.Fatal(err)
	}
	if err := manager.ReconcileOnStart(); !errors.Is(err, ErrRecoveryNeeded) {
		t.Fatalf("restart reconciliation error = %v", err)
	}
	status, _ := manager.Status()
	if status != StateRecoveryRequired {
		t.Fatalf("status = %q", status)
	}

	files = newMemoryFiles()
	now = time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	manager = newTestManager(files, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x55}, 32)))
	_, _, _ = manager.Initialize()
	now = now.Add(31 * time.Minute)
	if err := manager.ReconcileOnStart(); !errors.Is(err, ErrRecoveryNeeded) {
		t.Fatalf("expired code reconciliation error = %v", err)
	}
}
