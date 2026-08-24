package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"probe-api/internal/setup"
	"probe-api/internal/setupfinalize"
	"probe-api/internal/setupipc"
)

const (
	defaultStateFile = "/var/lib/probe-panel/setup/state.json"
	defaultCodeFile  = "/var/lib/probe-panel/setup/setup-code.json"
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
		return runPrivilegedFinalizer()
	}
	files := setup.NewRootFileStore()
	statePath := envString("PROBE_SETUP_STATE_FILE", defaultStateFile)
	codePath := envString("PROBE_SETUP_CODE_FILE", defaultCodeFile)
	states, err := setup.NewStateStore(files, statePath)
	if err != nil {
		return err
	}
	manager, err := setup.NewManager(states, files, codePath)
	if err != nil {
		return err
	}
	if args[0] == "init" {
		code, _, err := manager.Initialize()
		if err != nil {
			return fmt.Errorf("initialize setup: %w", err)
		}
		// The installer captures this single line and displays it directly to
		// the administrator. It is never written to a file or structured log.
		fmt.Println(code)
		return nil
	}

	if err := manager.ReconcileOnStart(); err != nil && !errors.Is(err, setup.ErrRecoveryNeeded) {
		return fmt.Errorf("reconcile setup state: %w", err)
	}
	if finalizer == nil {
		finalizer = setupipc.NewBroker()
	}
	logger := newLogger(envString("PROBE_SETUP_LOG_LEVEL", "info"))
	server, err := setup.NewServer(setup.ServerConfig{
		ListenAddress: envString("PROBE_SETUP_LISTEN_ADDR", setup.DefaultListenAddress),
		AdminRoot:     strings.TrimSpace(os.Getenv("PROBE_SETUP_ADMIN_ROOT")),
	}, logger, manager, finalizer)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errChannel := make(chan error, 1)
	go func() { errChannel <- server.ListenAndServe() }()
	logger.Info("setup server listening", "address", setup.DefaultListenAddress)
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

func runPrivilegedFinalizer() error {
	requestPath, err := fixedIPCPath("PROBE_SETUP_FINALIZE_REQUEST_FILE", setupipc.DefaultRequestPath)
	if err != nil {
		return err
	}
	resultPath, err := fixedIPCPath("PROBE_SETUP_FINALIZE_RESULT_FILE", setupipc.DefaultResultPath)
	if err != nil {
		return err
	}
	bundleRoot, err := requiredEnvironment("PROBE_SETUP_BUNDLE_ROOT")
	if err != nil {
		return err
	}
	releaseID, err := requiredEnvironment("PROBE_SETUP_RELEASE_ID")
	if err != nil {
		return err
	}

	request, err := setupipc.ReadRequest(requestPath)
	if err != nil {
		return errors.New("consume privileged setup request: setup IPC is unavailable")
	}
	defer request.ClearSecrets()

	finalizer, err := setupfinalize.New(setupfinalize.Config{
		BundleRoot:  bundleRoot,
		ReleaseID:   releaseID,
		RequireRoot: true,
	})
	result := setupipc.Result{Success: false, ErrorCode: setupipc.ErrorCodeInternal}
	if err == nil {
		err = finalizer.Finalize(context.Background(), request)
		if err == nil {
			result = setupipc.Result{Success: true}
		}
	}
	if writeErr := setupipc.WriteResult(resultPath, result); writeErr != nil {
		return errors.New("publish privileged setup result: setup IPC is unavailable")
	}
	if err != nil {
		// Finalizer errors are intentionally constructed without submitted
		// passwords. The HTTP process receives only the closed error code above.
		newLogger(envString("PROBE_SETUP_LOG_LEVEL", "info")).Error("privileged setup finalization failed", "error", err)
	}
	return nil
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
