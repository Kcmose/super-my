package main

import (
	"strings"
	"testing"

	"probe-api/internal/setupipc"
)

func TestRunRejectsUnknownCommandsBeforeSideEffects(t *testing.T) {
	for _, arguments := range [][]string{nil, {}, {"unknown"}, {"serve", "extra"}} {
		if err := run(arguments, nil); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("run(%q) error = %v, want usage error", arguments, err)
		}
	}
}

func TestFixedIPCPathCannotBeRedirected(t *testing.T) {
	t.Setenv("PROBE_SETUP_FINALIZE_REQUEST_FILE", setupipc.DefaultRequestPath)
	if value, err := fixedIPCPath("PROBE_SETUP_FINALIZE_REQUEST_FILE", setupipc.DefaultRequestPath); err != nil || value != setupipc.DefaultRequestPath {
		t.Fatalf("fixedIPCPath() = %q, %v", value, err)
	}

	t.Setenv("PROBE_SETUP_FINALIZE_REQUEST_FILE", "/tmp/finalize.json")
	if _, err := fixedIPCPath("PROBE_SETUP_FINALIZE_REQUEST_FILE", setupipc.DefaultRequestPath); err == nil {
		t.Fatal("fixedIPCPath accepted a redirected privileged request path")
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
