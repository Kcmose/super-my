package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
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
)

func main() {
	if err := run(os.Args[1:], nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, finalizer setup.Finalizer) error {
	if len(args) != 1 || (args[0] != "init" && args[0] != "serve" && args[0] != "finalize") {
		return errors.New("usage: probe-setup <init|serve|finalize>")
	}
	if args[0] == "finalize" {
		finalizeContext, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stopSignals()
		return runPrivilegedFinalizer(finalizeContext)
	}
	files := setup.NewRootFileStore()
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
	listener, err := activatedSystemdListener()
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

func activatedSystemdListener() (net.Listener, error) {
	if err := validateSystemdActivation(os.Getpid(), os.Getenv("LISTEN_PID"), os.Getenv("LISTEN_FDS"), os.Getenv("LISTEN_FDNAMES")); err != nil {
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

func validateSystemdActivation(processID int, listenPIDValue, listenFDsValue, listenFDNames string) error {
	listenPID, err := strconv.Atoi(strings.TrimSpace(listenPIDValue))
	if err != nil || listenPID != processID {
		return errors.New("setup service requires a systemd socket for this process")
	}
	listenFDs, err := strconv.Atoi(strings.TrimSpace(listenFDsValue))
	if err != nil || listenFDs != 1 {
		return errors.New("setup service requires exactly one systemd socket")
	}
	if listenFDNames != setupFDName {
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
	return &setup.SetupDefaults{
		ServerIP: address.String(),
		PanelURL: origin(setup.PanelHTTPSPort),
		AgentURL: origin(setup.AgentHTTPSPort),
		AdminURL: origin(setup.AdminHTTPSPort),
	}, nil
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

	finalizer, err := setupfinalize.New(setupfinalize.Config{
		BundleRoot:  bundleRoot,
		ReleaseID:   releaseID,
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
	return failure, nil
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
