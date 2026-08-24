package main

import (
	"context"
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"probe-api/internal/setup"
	"probe-api/internal/setupipc"
)

type mainMemoryFiles struct {
	mu    sync.Mutex
	files map[string][]byte
}

func newMainMemoryFiles() *mainMemoryFiles {
	return &mainMemoryFiles{files: make(map[string][]byte)}
}

func (files *mainMemoryFiles) Read(path string, maxBytes int64) ([]byte, error) {
	files.mu.Lock()
	defer files.mu.Unlock()
	contents, ok := files.files[path]
	if !ok {
		return nil, setup.ErrFileNotFound
	}
	if int64(len(contents)) > maxBytes {
		return nil, errors.New("test file exceeds limit")
	}
	return append([]byte(nil), contents...), nil
}

func (files *mainMemoryFiles) CreateAtomic(path string, contents []byte) error {
	files.mu.Lock()
	defer files.mu.Unlock()
	if _, exists := files.files[path]; exists {
		return fs.ErrExist
	}
	files.files[path] = append([]byte(nil), contents...)
	return nil
}

func (files *mainMemoryFiles) WriteAtomic(path string, contents []byte) error {
	files.mu.Lock()
	defer files.mu.Unlock()
	files.files[path] = append([]byte(nil), contents...)
	return nil
}

func (files *mainMemoryFiles) Remove(path string) error {
	files.mu.Lock()
	defer files.mu.Unlock()
	if _, exists := files.files[path]; !exists {
		return setup.ErrFileNotFound
	}
	delete(files.files, path)
	return nil
}

func TestRunRejectsUnknownCommandsBeforeSideEffects(t *testing.T) {
	for _, arguments := range [][]string{nil, {}, {"unknown"}, {"serve", "extra"}} {
		if err := run(arguments, nil); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("run(%q) error = %v, want usage error", arguments, err)
		}
	}
}

func TestSystemdActivationEnvironmentIsExact(t *testing.T) {
	const processID = 4242
	validPID := strconv.Itoa(processID)
	if err := validateSystemdActivation(processID, validPID, "1", setupFDName); err != nil {
		t.Fatalf("valid activation: %v", err)
	}
	for name, values := range map[string][3]string{
		"missing pid":       {"", "1", setupFDName},
		"wrong pid":         {"4243", "1", setupFDName},
		"multiple fds":      {validPID, "2", setupFDName},
		"missing fd":        {validPID, "0", setupFDName},
		"wrong fd name":     {validPID, "1", "probe-panel-setup.socket"},
		"multiple fd names": {validPID, "1", setupFDName + ":extra"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateSystemdActivation(processID, values[0], values[1], values[2]); err == nil {
				t.Fatal("invalid activation environment was accepted")
			}
		})
	}
}

func TestSetupDefaultsFromServerIP(t *testing.T) {
	for name, test := range map[string]struct {
		input    string
		panelURL string
		agentURL string
		adminURL string
	}{
		"IPv4": {input: "192.0.2.10", panelURL: "https://192.0.2.10:18453", agentURL: "https://192.0.2.10:18454", adminURL: "https://192.0.2.10:18455"},
		"IPv6": {input: "2001:db8::10", panelURL: "https://[2001:db8::10]:18453", agentURL: "https://[2001:db8::10]:18454", adminURL: "https://[2001:db8::10]:18455"},
	} {
		t.Run(name, func(t *testing.T) {
			defaults, err := setupDefaultsFromServerIP(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if defaults.ServerIP != test.input || defaults.PanelURL != test.panelURL || defaults.AgentURL != test.agentURL || defaults.AdminURL != test.adminURL {
				t.Fatalf("defaults = %#v", defaults)
			}
		})
	}
	for _, input := range []string{"", "localhost", "127.0.0.1", "2001:0db8::10", "::ffff:192.0.2.10"} {
		if _, err := setupDefaultsFromServerIP(input); err == nil {
			t.Fatalf("invalid server IP %q was accepted", input)
		}
	}
}

func TestSetupSocketPathEnvironmentIsRead(t *testing.T) {
	t.Setenv("PROBE_SETUP_SOCKET_PATH", "/tmp/redirected.sock")
	if value := envString("PROBE_SETUP_SOCKET_PATH", "/run/probe-panel-setup/setup.sock"); value != "/tmp/redirected.sock" {
		t.Fatalf("socket environment value = %q", value)
	}
}

func TestFixedIPCPathCannotBeRedirected(t *testing.T) {
	t.Setenv("PROBE_SETUP_STATE_FILE", defaultStateFile)
	if value, err := fixedIPCPath("PROBE_SETUP_STATE_FILE", defaultStateFile); err != nil || value != defaultStateFile {
		t.Fatalf("fixed state path = %q, %v", value, err)
	}
	t.Setenv("PROBE_SETUP_STATE_FILE", "/tmp/state.json")
	if _, err := fixedIPCPath("PROBE_SETUP_STATE_FILE", defaultStateFile); err == nil {
		t.Fatal("fixedIPCPath accepted a redirected privileged state path")
	}

	t.Setenv("PROBE_SETUP_FINALIZE_REQUEST_FILE", setupipc.DefaultRequestPath)
	if value, err := fixedIPCPath("PROBE_SETUP_FINALIZE_REQUEST_FILE", setupipc.DefaultRequestPath); err != nil || value != setupipc.DefaultRequestPath {
		t.Fatalf("fixedIPCPath() = %q, %v", value, err)
	}

	t.Setenv("PROBE_SETUP_FINALIZE_REQUEST_FILE", "/tmp/finalize.json")
	if _, err := fixedIPCPath("PROBE_SETUP_FINALIZE_REQUEST_FILE", setupipc.DefaultRequestPath); err == nil {
		t.Fatal("fixedIPCPath accepted a redirected privileged request path")
	}

	t.Setenv("PROBE_SETUP_FINALIZE_RESULT_FILE", setupipc.DefaultResultPath)
	if value, err := fixedIPCPath("PROBE_SETUP_FINALIZE_RESULT_FILE", setupipc.DefaultResultPath); err != nil || value != setupipc.DefaultResultPath {
		t.Fatalf("fixed result path = %q, %v", value, err)
	}
	t.Setenv("PROBE_SETUP_FINALIZE_RESULT_FILE", "/tmp/result.json")
	if _, err := fixedIPCPath("PROBE_SETUP_FINALIZE_RESULT_FILE", setupipc.DefaultResultPath); err == nil {
		t.Fatal("fixedIPCPath accepted a redirected privileged result path")
	}
}

func TestPrivilegedFinalizerReconciliationUsesPersistentTerminalState(t *testing.T) {
	newFinalizingStore := func(t *testing.T) (*setup.StateStore, time.Time) {
		t.Helper()
		now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
		store, err := setup.NewStateStore(newMainMemoryFiles(), "/state.json")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Initialize(now); err != nil {
			t.Fatal(err)
		}
		if err := store.Transition(setup.StatePending, setup.StateConfiguring, now); err != nil {
			t.Fatal(err)
		}
		if err := store.Transition(setup.StateConfiguring, setup.StateFinalizing, now); err != nil {
			t.Fatal(err)
		}
		return store, now
	}

	t.Run("failure marks recovery before the failure result", func(t *testing.T) {
		store, now := newFinalizingStore(t)
		result, err := reconcilePrivilegedFinalizerState(store, errors.New("finalizer failed"), now.Add(time.Second))
		if err != nil || result.Success || result.ErrorCode != setupipc.ErrorCodeInternal {
			t.Fatalf("result = %#v, error = %v", result, err)
		}
		record, loadErr := store.Load()
		if loadErr != nil || record.Status != setup.StateRecoveryRequired {
			t.Fatalf("terminal state = %#v, error = %v", record, loadErr)
		}
	})

	t.Run("success result without root commit fails closed", func(t *testing.T) {
		store, now := newFinalizingStore(t)
		result, err := reconcilePrivilegedFinalizerState(store, nil, now.Add(time.Second))
		if err == nil || result.Success {
			t.Fatalf("result = %#v, error = %v", result, err)
		}
		record, loadErr := store.Load()
		if loadErr != nil || record.Status != setup.StateRecoveryRequired {
			t.Fatalf("terminal state = %#v, error = %v", record, loadErr)
		}
	})

	t.Run("installed wins over a late transport failure", func(t *testing.T) {
		store, now := newFinalizingStore(t)
		if err := store.Transition(setup.StateFinalizing, setup.StateInstalled, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		result, err := reconcilePrivilegedFinalizerState(store, errors.New("lost result"), now.Add(2*time.Second))
		if err != nil || !result.Success || result.ErrorCode != "" {
			t.Fatalf("result = %#v, error = %v", result, err)
		}
		record, loadErr := store.Load()
		if loadErr != nil || record.Status != setup.StateInstalled {
			t.Fatalf("terminal state = %#v, error = %v", record, loadErr)
		}
	})
}

func TestPrivilegedFinalizerDeadlineLeavesIndependentCleanupWindow(t *testing.T) {
	if privilegedFinalizeTimeout+privilegedFinalizeCleanupBudget >= setupipc.DefaultMaximumWait {
		t.Fatalf("worker timeout %s plus cleanup budget %s must precede broker timeout %s", privilegedFinalizeTimeout, privilegedFinalizeCleanupBudget, setupipc.DefaultMaximumWait)
	}
	if privilegedFinalizeTimeout >= 30*time.Minute {
		t.Fatal("worker timeout must precede the finalizer systemd unit timeout")
	}

	workerFailure := errors.New("worker failure")
	deadlineChecked := false
	err := executePrivilegedFinalizer(
		context.Background(),
		func(finalizeContext context.Context) error {
			deadline, ok := finalizeContext.Deadline()
			if !ok {
				t.Fatal("privileged finalizer worker has no deadline")
			}
			remaining := time.Until(deadline)
			if remaining <= 0 || remaining > privilegedFinalizeTimeout {
				t.Fatalf("privileged finalizer worker deadline leaves %s", remaining)
			}
			deadlineChecked = true
			return workerFailure
		},
		func(finalizeErr error) error {
			if !errors.Is(finalizeErr, workerFailure) {
				t.Fatalf("published finalizer error = %v; want worker failure", finalizeErr)
			}
			return nil
		},
	)
	if err != nil || !deadlineChecked {
		t.Fatalf("privileged finalizer deadline execution = %v, checked = %v", err, deadlineChecked)
	}
}

func TestPrivilegedFinalizerCancellationWaitsForCleanupBeforePublishing(t *testing.T) {
	parentContext, terminate := context.WithCancel(context.Background())
	started := make(chan struct{})
	completed := make(chan error, 1)
	var order []string

	go func() {
		completed <- executePrivilegedFinalizer(
			parentContext,
			func(finalizeContext context.Context) error {
				close(started)
				<-finalizeContext.Done()
				order = append(order, "rollback")
				return finalizeContext.Err()
			},
			func(finalizeErr error) error {
				if !errors.Is(finalizeErr, context.Canceled) {
					t.Errorf("published finalizer error = %v; want context cancellation", finalizeErr)
				}
				order = append(order, "terminal-state", "result")
				return nil
			},
		)
	}()

	<-started
	terminate()
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("privileged finalizer did not preserve its cancellation cleanup budget")
	}
	if got := strings.Join(order, ","); got != "rollback,terminal-state,result" {
		t.Fatalf("cancellation order = %q", got)
	}
}

func TestRequiredEnvironmentRejectsBlankValues(t *testing.T) {
	t.Setenv("PROBE_SETUP_BUNDLE_ROOT", "  ")
	if _, err := requiredEnvironment("PROBE_SETUP_BUNDLE_ROOT"); err == nil {
		t.Fatal("requiredEnvironment accepted a blank value")
	}

	t.Setenv("PROBE_SETUP_BUNDLE_ROOT", "/srv/probe/releases/test")
	if value, err := requiredEnvironment("PROBE_SETUP_BUNDLE_ROOT"); err != nil || value != "/srv/probe/releases/test" {
		t.Fatalf("requiredEnvironment() = %q, %v", value, err)
	}
}
