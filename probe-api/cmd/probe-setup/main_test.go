package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"probe-api/internal/setup"
	"probe-api/internal/setupfinalize"
	"probe-api/internal/setupipc"
)

type mainMemoryFiles struct {
	mu    sync.Mutex
	files map[string][]byte
}

type sequenceStateLoader struct {
	states []setup.State
	index  int
}

func (loader *sequenceStateLoader) Load() (setup.StateRecord, error) {
	if loader == nil || len(loader.states) == 0 {
		return setup.StateRecord{}, errors.New("state unavailable")
	}
	index := loader.index
	if index >= len(loader.states) {
		index = len(loader.states) - 1
	} else {
		loader.index++
	}
	return setup.StateRecord{Version: 1, Status: loader.states[index]}, nil
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

func TestRunRequiresManagementBeforeEveryCommand(t *testing.T) {
	originalFactory := newSetupRootFiles
	files := newMainMemoryFiles()
	newSetupRootFiles = func() setup.SecureFiles { return files }
	t.Cleanup(func() { newSetupRootFiles = originalFactory })
	t.Setenv("PROBE_SETUP_STATE_FILE", "/state.json")

	for _, profile := range []string{"", "full"} {
		t.Setenv("PROBE_SETUP_PROFILE", profile)
		for _, command := range []string{"init", "serve", "finalize", "finalize-cleanup"} {
			if err := run([]string{command}, nil); err == nil || !strings.Contains(err.Error(), "must be exactly management") {
				t.Fatalf("run(%q) with profile %q error = %v", command, profile, err)
			}
		}
	}

	t.Setenv("PROBE_SETUP_PROFILE", "management")
	if err := run([]string{"init"}, nil); err != nil {
		t.Fatalf("management init failed: %v", err)
	}
}

func TestSystemdActivationEnvironmentIsExact(t *testing.T) {
	const processID = 4242
	validPID := strconv.Itoa(processID)
	if err := validateSystemdActivation(processID, validPID, "1", setupFDName, setupfinalize.PlatformDebian13Systemd); err != nil {
		t.Fatalf("valid activation: %v", err)
	}
	if err := validateSystemdActivation(processID, validPID, "1", "", setupfinalize.PlatformCentOSLinux7Systemd); err != nil {
		t.Fatalf("legacy unnamed activation: %v", err)
	}
	if err := validateSystemdActivation(processID, validPID, "1", "", setupfinalize.PlatformDebian13Systemd); err == nil {
		t.Fatal("modern platform accepted an unnamed socket descriptor")
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
			if err := validateSystemdActivation(processID, values[0], values[1], values[2], setupfinalize.PlatformDebian13Systemd); err == nil {
				t.Fatal("invalid activation environment was accepted")
			}
		})
	}
	if err := validateSystemdActivation(processID, validPID, "1", setupFDName, "unknown-systemd"); err == nil {
		t.Fatal("unknown platform accepted a socket descriptor")
	}
}

func TestSetupDefaultsFromServerIP(t *testing.T) {
	for name, test := range map[string]struct {
		input    string
		adminURL string
	}{
		"IPv4": {input: "192.0.2.10", adminURL: "https://192.0.2.10:18455"},
		"IPv6": {input: "2001:db8::10", adminURL: "https://[2001:db8::10]:18455"},
	} {
		t.Run(name, func(t *testing.T) {
			defaults, err := setupDefaultsFromServerIP(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if defaults.Profile != setup.InstallationProfileManagement || defaults.ServerIP != test.input ||
				defaults.PanelURL != "" || defaults.AgentURL != "" || defaults.AdminURL != test.adminURL {
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

func TestSetupProfileRequiresExplicitManagement(t *testing.T) {
	for _, value := range []string{"", "full", "unknown", " management "} {
		t.Setenv("PROBE_SETUP_PROFILE", value)
		if _, err := requiredManagementSetupProfile(); err == nil {
			t.Fatalf("setup profile %q was accepted", value)
		}
	}
	t.Setenv("PROBE_SETUP_PROFILE", "management")
	if profile, err := requiredManagementSetupProfile(); err != nil || profile != setup.InstallationProfileManagement {
		t.Fatalf("management profile = %q, %v", profile, err)
	}
}

func TestSetupPlatformRequiresExplicitSupportedContract(t *testing.T) {
	for _, platformID := range []string{
		setupfinalize.PlatformDebian9Systemd,
		setupfinalize.PlatformDebian10Systemd,
		setupfinalize.PlatformDebian11Systemd,
		setupfinalize.PlatformDebian12Systemd,
		setupfinalize.PlatformDebian13Systemd,
		setupfinalize.PlatformUbuntu1804Systemd,
		setupfinalize.PlatformUbuntu2004Systemd,
		setupfinalize.PlatformUbuntu2204Systemd,
		setupfinalize.PlatformUbuntu2404Systemd,
		setupfinalize.PlatformUbuntu2604Systemd,
		setupfinalize.PlatformCentOSLinux7Systemd,
		setupfinalize.PlatformCentOSLinux8Systemd,
		setupfinalize.PlatformCentOSStream8Systemd,
		setupfinalize.PlatformCentOSStream9Systemd,
		setupfinalize.PlatformCentOSStream10Systemd,
	} {
		t.Setenv("PROBE_SETUP_PLATFORM_ID", platformID)
		value, err := requiredSetupPlatformID()
		if err != nil || value != platformID {
			t.Fatalf("setup platform %q = %q, %v", platformID, value, err)
		}
	}

	for _, value := range []string{"", "unknown-systemd", " debian-13-systemd", "debian-13-systemd "} {
		t.Setenv("PROBE_SETUP_PLATFORM_ID", value)
		if _, err := requiredSetupPlatformID(); err == nil {
			t.Fatalf("unsupported setup platform %q was accepted", value)
		}
	}
}

func TestPrivilegedFinalizerRejectsNonManagementRequest(t *testing.T) {
	for _, profile := range []setup.InstallationProfile{"", setup.InstallationProfileFull, "unknown"} {
		if err := validatePrivilegedManagementRequest(setup.CompleteRequest{Profile: profile}); err == nil {
			t.Fatalf("privileged profile %q was accepted", profile)
		}
	}
	if err := validatePrivilegedManagementRequest(setup.CompleteRequest{Profile: setup.InstallationProfileManagement}); err != nil {
		t.Fatalf("management request rejected: %v", err)
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

	t.Run("preflight failure returns to configuring for an explicit retry", func(t *testing.T) {
		store, now := newFinalizingStore(t)
		preflightErr := fmt.Errorf("%w: occupied management port", setupfinalize.ErrPreflight)
		result, err := reconcilePrivilegedFinalizerState(store, preflightErr, now.Add(time.Second))
		if err != nil || result.Success || result.ErrorCode != setupipc.ErrorCodePreflightFailed {
			t.Fatalf("result = %#v, error = %v", result, err)
		}
		record, loadErr := store.Load()
		if loadErr != nil || record.Status != setup.StateConfiguring {
			t.Fatalf("retryable state = %#v, error = %v", record, loadErr)
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

func TestFinalizerCleanupPreservesRetryableSetupAndStopsOnlyTerminalSetup(t *testing.T) {
	if terminalSetupCleanupDelay <= 25*time.Second {
		t.Fatalf("terminal cleanup delay %s must preserve the installed handoff window", terminalSetupCleanupDelay)
	}
	t.Run("configuring remains available", func(t *testing.T) {
		var commands []string
		err := cleanupTerminalSetup(context.Background(), &sequenceStateLoader{
			states: []setup.State{setup.StateConfiguring},
		}, 0, func(_ context.Context, name string, arguments ...string) error {
			commands = append(commands, strings.Join(append([]string{name}, arguments...), " "))
			return nil
		})
		if err != nil || len(commands) != 0 {
			t.Fatalf("retryable cleanup error=%v commands=%v", err, commands)
		}
	})

	t.Run("installed stops socket then service", func(t *testing.T) {
		var commands []string
		err := cleanupTerminalSetup(context.Background(), &sequenceStateLoader{
			states: []setup.State{setup.StateInstalled, setup.StateInstalled},
		}, 0, func(_ context.Context, name string, arguments ...string) error {
			commands = append(commands, strings.Join(append([]string{name}, arguments...), " "))
			return nil
		})
		want := []string{
			"/usr/bin/systemctl stop probe-panel-setup.socket",
			"/usr/bin/systemctl --no-block stop probe-panel-setup.service",
		}
		if err != nil || strings.Join(commands, "\n") != strings.Join(want, "\n") {
			t.Fatalf("terminal cleanup error=%v commands=%v", err, commands)
		}
	})

	t.Run("terminal state is rechecked after handoff delay", func(t *testing.T) {
		var commands []string
		err := cleanupTerminalSetup(context.Background(), &sequenceStateLoader{
			states: []setup.State{setup.StateRecoveryRequired, setup.StateConfiguring},
		}, 0, func(_ context.Context, name string, arguments ...string) error {
			commands = append(commands, strings.Join(append([]string{name}, arguments...), " "))
			return nil
		})
		if err != nil || len(commands) != 0 {
			t.Fatalf("changed state cleanup error=%v commands=%v", err, commands)
		}
	})
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
