package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"probe-api/internal/setup"
	"probe-api/internal/setupfinalize"
	"probe-api/internal/setupipc"
)

const (
	defaultStateFile = "/var/lib/probe-panel/setup/state.json"
	systemdFDStart   = uintptr(3)
	setupFDName      = "setup-http"
	// The worker deadline deliberately precedes both the broker and systemd
	// 30-minute deadlines. This minute is reserved for independent rollback,
	// durable terminal-state reconciliation, and result publication.
	privilegedFinalizeTimeout       = 25 * time.Minute
	privilegedFinalizeCleanupBudget = time.Minute
	terminalSetupCleanupDelay       = 30 * time.Second
)

var newSetupRootFiles = func() setup.SecureFiles { return setup.NewRootFileStore() }

func main() {
	if err := run(os.Args[1:], nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, finalizer setup.Finalizer) error {
	if len(args) != 1 || (args[0] != "init" && args[0] != "serve" && args[0] != "finalize" && args[0] != "finalize-cleanup") {
		return errors.New("usage: probe-setup <init|serve|finalize|finalize-cleanup>")
	}
	if _, err := requiredManagementSetupProfile(); err != nil {
		return err
	}
	if args[0] == "finalize" || args[0] == "finalize-cleanup" {
		finalizeContext, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stopSignals()
		if args[0] == "finalize-cleanup" {
			return runFinalizerCleanup(finalizeContext)
		}
		return runPrivilegedFinalizer(finalizeContext)
	}
	files := newSetupRootFiles()
	statePath := envString("PROBE_SETUP_STATE_FILE", defaultStateFile)
	states, err := setup.NewStateStore(files, statePath)
	if err != nil {
		return err
	}
	manager, err := setup.NewManager(states)
	if err != nil {
		return err
	}
	if args[0] == "init" {
		if err := manager.Initialize(); err != nil {
			return fmt.Errorf("initialize setup: %w", err)
		}
		return nil
	}

	if err := manager.ReconcileOnStart(); err != nil && !errors.Is(err, setup.ErrRecoveryNeeded) {
		return fmt.Errorf("reconcile setup state: %w", err)
	}
	if finalizer == nil {
		finalizer = setupipc.NewBroker()
	}
	logger := newLogger(envString("PROBE_SETUP_LOG_LEVEL", "info"))
	defaults, err := setupDefaultsFromServerIP(os.Getenv("PROBE_SETUP_SERVER_IP"))
	if err != nil {
		return err
	}
	socketPath := envString("PROBE_SETUP_SOCKET_PATH", setup.DefaultSocketPath)
	server, err := setup.NewServer(setup.ServerConfig{
		SocketPath: socketPath,
		AdminRoot:  strings.TrimSpace(os.Getenv("PROBE_SETUP_ADMIN_ROOT")),
		Defaults:   defaults,
	}, logger, manager, finalizer)
	if err != nil {
		return err
	}
	platformID, err := requiredSetupPlatformID()
	if err != nil {
		return err
	}
	listener, err := activatedSystemdListener(platformID)
	if err != nil {
		return err
	}
	defer listener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errChannel := make(chan error, 1)
	go func() { errChannel <- server.Serve(listener) }()
	logger.Info("setup server ready", "socket", socketPath)
	select {
	case err := <-errChannel:
		if errors.Is(err, setup.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve setup HTTP: %w", err)
	case <-ctx.Done():
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		return fmt.Errorf("shut down setup HTTP: %w", err)
	}
	return nil
}

type setupStateLoader interface {
	Load() (setup.StateRecord, error)
}

type cleanupCommand func(context.Context, string, ...string) error

func runFinalizerCleanup(ctx context.Context) error {
	if os.Geteuid() != 0 {
		return errors.New("setup finalizer cleanup must run as root")
	}
	statePath, err := fixedIPCPath("PROBE_SETUP_STATE_FILE", defaultStateFile)
	if err != nil {
		return err
	}
	states, err := setup.NewStateStore(setup.NewRootFileStore(), statePath)
	if err != nil {
		return err
	}
	runCommand := func(commandContext context.Context, name string, arguments ...string) error {
		return exec.CommandContext(commandContext, name, arguments...).Run()
	}
	return cleanupTerminalSetup(ctx, states, terminalSetupCleanupDelay, runCommand)
}

func cleanupTerminalSetup(ctx context.Context, states setupStateLoader, delay time.Duration, runCommand cleanupCommand) error {
	if ctx == nil || states == nil || runCommand == nil {
		return errors.New("setup finalizer cleanup is not configured")
	}
	record, err := states.Load()
	if err != nil {
		return fmt.Errorf("inspect setup state before finalizer cleanup: %w", err)
	}
	if !terminalSetupState(record.Status) {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return ctx.Err()
	}
	record, err = states.Load()
	if err != nil {
		return fmt.Errorf("recheck setup state before finalizer cleanup: %w", err)
	}
	if !terminalSetupState(record.Status) {
		return nil
	}
	var failures []error
	if err := runCommand(ctx, "/usr/bin/systemctl", "stop", "probe-panel-setup.socket"); err != nil {
		failures = append(failures, fmt.Errorf("stop terminal setup socket: %w", err))
	}
	if err := runCommand(ctx, "/usr/bin/systemctl", "--no-block", "stop", "probe-panel-setup.service"); err != nil {
		failures = append(failures, fmt.Errorf("stop terminal setup service: %w", err))
	}
	return errors.Join(failures...)
}

func terminalSetupState(state setup.State) bool {
	return state == setup.StateInstalled || state == setup.StateRecoveryRequired
}

func activatedSystemdListener(platformID string) (net.Listener, error) {
	if err := validateSystemdActivation(os.Getpid(), os.Getenv("LISTEN_PID"), os.Getenv("LISTEN_FDS"), os.Getenv("LISTEN_FDNAMES"), platformID); err != nil {
		return nil, err
	}
	for _, name := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		if err := os.Unsetenv(name); err != nil {
			return nil, fmt.Errorf("clear systemd socket environment: %w", err)
		}
	}

	file := os.NewFile(systemdFDStart, setupFDName)
	if file == nil {
		return nil, errors.New("setup systemd socket descriptor is unavailable")
	}
	listener, listenerErr := net.FileListener(file)
	closeErr := file.Close()
	if listenerErr != nil {
		return nil, fmt.Errorf("adopt setup systemd socket: %w", listenerErr)
	}
	if closeErr != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("close inherited setup socket descriptor: %w", closeErr)
	}
	return listener, nil
}

func validateSystemdActivation(processID int, listenPIDValue, listenFDsValue, listenFDNames, platformID string) error {
	listenPID, err := strconv.Atoi(strings.TrimSpace(listenPIDValue))
	if err != nil || listenPID != processID {
		return errors.New("setup service requires a systemd socket for this process")
	}
	listenFDs, err := strconv.Atoi(strings.TrimSpace(listenFDsValue))
	if err != nil || listenFDs != 1 {
		return errors.New("setup service requires exactly one systemd socket")
	}
	unitProfile, err := setupfinalize.UnitProfileForPlatformID(platformID)
	if err != nil {
		return errors.New("setup service received an unsupported platform contract")
	}
	if listenFDNames != setupFDName && !(unitProfile == setupfinalize.SystemdUnitProfileLegacy && listenFDNames == "") {
		return errors.New("setup service received an unexpected systemd socket")
	}
	return nil
}

func setupDefaultsFromServerIP(value string) (*setup.SetupDefaults, error) {
	value = strings.TrimSpace(value)
	address, err := netip.ParseAddr(value)
	if err != nil || value == "" || address.String() != value || address.Zone() != "" || address.Is4In6() || !address.IsGlobalUnicast() {
		return nil, errors.New("PROBE_SETUP_SERVER_IP must be a canonical routable IPv4 or IPv6 address")
	}
	origin := func(port int) string {
		return "https://" + net.JoinHostPort(address.String(), strconv.Itoa(port))
	}
	defaults := &setup.SetupDefaults{
		Profile:  setup.InstallationProfileManagement,
		ServerIP: address.String(),
		AdminURL: origin(setup.AdminHTTPSPort),
	}
	return defaults, nil
}

func requiredManagementSetupProfile() (setup.InstallationProfile, error) {
	value := os.Getenv("PROBE_SETUP_PROFILE")
	if value != string(setup.InstallationProfileManagement) {
		return "", errors.New("PROBE_SETUP_PROFILE must be exactly management")
	}
	return setup.InstallationProfileManagement, nil
}

func requiredSetupPlatformID() (string, error) {
	value := os.Getenv("PROBE_SETUP_PLATFORM_ID")
	if value == "" {
		return "", errors.New("PROBE_SETUP_PLATFORM_ID is required")
	}
	if err := setupfinalize.ValidatePlatformID(value); err != nil {
		return "", fmt.Errorf("PROBE_SETUP_PLATFORM_ID is invalid: %w", err)
	}
	return value, nil
}

func validatePrivilegedManagementRequest(request setup.CompleteRequest) error {
	if request.Profile != setup.InstallationProfileManagement {
		return errors.New("privileged setup request must be exactly management")
	}
	return nil
}

func runPrivilegedFinalizer(parentContext context.Context) error {
	if parentContext == nil {
		return errors.New("privileged finalizer context is required")
	}
	statePath, err := fixedIPCPath("PROBE_SETUP_STATE_FILE", defaultStateFile)
	if err != nil {
		return err
	}
	states, err := setup.NewStateStore(setup.NewRootFileStore(), statePath)
	if err != nil {
		return err
	}
	// Always publish only to the reviewed root-private result path. If its
	// environment override is invalid, this fixed fallback still lets a waiting
	// broker observe the closed failure after recovery has been persisted.
	resultPath := setupipc.DefaultResultPath
	fail := func(cause error) error {
		return publishPrivilegedFinalizerOutcome(states, resultPath, cause)
	}
	if _, err := requiredManagementSetupProfile(); err != nil {
		return fail(err)
	}
	platformID, err := requiredSetupPlatformID()
	if err != nil {
		return fail(err)
	}
	requestPath, err := fixedIPCPath("PROBE_SETUP_FINALIZE_REQUEST_FILE", setupipc.DefaultRequestPath)
	if err != nil {
		return fail(err)
	}
	validatedResultPath, err := fixedIPCPath("PROBE_SETUP_FINALIZE_RESULT_FILE", setupipc.DefaultResultPath)
	if err != nil {
		return fail(err)
	}
	resultPath = validatedResultPath
	bundleRoot, err := requiredEnvironment("PROBE_SETUP_BUNDLE_ROOT")
	if err != nil {
		return fail(err)
	}
	releaseID, err := requiredEnvironment("PROBE_SETUP_RELEASE_ID")
	if err != nil {
		return fail(err)
	}

	request, err := setupipc.ReadRequest(requestPath)
	if err != nil {
		return fail(errors.New("consume privileged setup request: setup IPC is unavailable"))
	}
	defer request.ClearSecrets()
	if err := validatePrivilegedManagementRequest(request); err != nil {
		return fail(err)
	}

	finalizer, err := setupfinalize.New(setupfinalize.Config{
		BundleRoot:  bundleRoot,
		ReleaseID:   releaseID,
		PlatformID:  platformID,
		RequireRoot: true,
		CommitInstalled: func(now time.Time) error {
			return states.Transition(setup.StateFinalizing, setup.StateInstalled, now.UTC())
		},
	})
	if err == nil {
		return executePrivilegedFinalizer(
			parentContext,
			func(finalizeContext context.Context) error {
				return finalizer.Finalize(finalizeContext, request)
			},
			func(finalizeErr error) error {
				return publishPrivilegedFinalizerOutcome(states, resultPath, finalizeErr)
			},
		)
	}
	return publishPrivilegedFinalizerOutcome(states, resultPath, err)
}

func executePrivilegedFinalizer(parentContext context.Context, finalize func(context.Context) error, publish func(error) error) error {
	finalizeContext, cancel := context.WithTimeout(parentContext, privilegedFinalizeTimeout)
	finalizeErr := finalize(finalizeContext)
	cancel()

	// A canceled Finalize returns only after its deferred, independently timed
	// production rollback has completed. Reconciliation and IPC publication are
	// deliberately outside the canceled worker context so the first termination
	// signal cannot interrupt either durable terminal outcome.
	return publish(finalizeErr)
}

func publishPrivilegedFinalizerOutcome(states *setup.StateStore, resultPath string, finalizeErr error) error {
	result, terminalErr := reconcilePrivilegedFinalizerState(states, finalizeErr, time.Now().UTC())
	if result.Success {
		// Installed is already durable; neither an IPC transport error nor a
		// stale failure from the caller may reverse that terminal state.
		finalizeErr = nil
	}
	writeErr := setupipc.WriteResult(resultPath, result)
	if finalizeErr != nil {
		// Finalizer errors are intentionally constructed without submitted
		// passwords. The HTTP process receives only the closed error code above.
		newLogger(envString("PROBE_SETUP_LOG_LEVEL", "info")).Error("privileged setup finalization failed", "error", finalizeErr)
	}
	var outcomeErrors []error
	if writeErr != nil {
		outcomeErrors = append(outcomeErrors, errors.New("publish privileged setup result: setup IPC is unavailable"))
	}
	if terminalErr != nil {
		outcomeErrors = append(outcomeErrors, errors.New("persist privileged setup terminal state"))
	}
	return errors.Join(outcomeErrors...)
}

func reconcilePrivilegedFinalizerState(states *setup.StateStore, finalizeErr error, now time.Time) (setupipc.Result, error) {
	failure := setupipc.Result{Success: false, ErrorCode: setupipc.ErrorCodeInternal}
	if states == nil {
		return failure, errors.New("setup state store is unavailable")
	}
	record, err := states.Load()
	if err != nil {
		return failure, err
	}
	if record.Status == setup.StateInstalled {
		return setupipc.Result{Success: true}, nil
	}
	var retryTransitionErr error
	if errors.Is(finalizeErr, setupfinalize.ErrPreflight) {
		if record.Status == setup.StateConfiguring {
			return setupipc.Result{Success: false, ErrorCode: setupipc.ErrorCodePreflightFailed}, nil
		}
		if record.Status == setup.StateFinalizing {
			if err := states.Transition(setup.StateFinalizing, setup.StateConfiguring, now.UTC()); err == nil {
				return setupipc.Result{Success: false, ErrorCode: setupipc.ErrorCodePreflightFailed}, nil
			} else {
				retryTransitionErr = fmt.Errorf("restore configuring state after preflight failure: %w", err)
			}
		}
	}
	if record.Status != setup.StateRecoveryRequired {
		if err := states.MarkRecovery(now.UTC()); err != nil {
			latest, loadErr := states.Load()
			if loadErr == nil && latest.Status == setup.StateInstalled {
				return setupipc.Result{Success: true}, nil
			}
			return failure, err
		}
	}
	if finalizeErr == nil {
		return failure, errors.New("privileged finalizer returned without an installed commit")
	}
	return failure, retryTransitionErr
}

func fixedIPCPath(name, expected string) (string, error) {
	value := envString(name, expected)
	if value != expected {
		return "", fmt.Errorf("%s must use the fixed private runtime path", name)
	}
	return value, nil
}

func requiredEnvironment(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func envString(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func newLogger(levelName string) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(levelName) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}
