//go:build linux

package setup

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestActivatedUnixListenerAndRootPeerBoundary(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "setup.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	validationErr := validateActivatedUnixListener(listener, path)
	if os.Geteuid() == 0 {
		if validationErr != nil {
			t.Fatalf("root-owned listener rejected: %v", validationErr)
		}
	} else if validationErr == nil {
		t.Fatal("non-root-owned listener passed the root ownership check")
	}

	accepted := make(chan net.Conn, 1)
	acceptErrors := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			acceptErrors <- err
			return
		}
		accepted <- connection
	}()
	client, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	select {
	case err := <-acceptErrors:
		t.Fatal(err)
	case connection := <-accepted:
		defer connection.Close()
		got := authorizedRootUnixConnection(connection, path)
		if got != (os.Geteuid() == 0) {
			t.Fatalf("authorized root peer = %v for euid %d", got, os.Geteuid())
		}
	case <-time.After(time.Second):
		t.Fatal("Unix listener did not accept the test connection")
	}

	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	if err := validateActivatedUnixListener(listener, path); err == nil {
		t.Fatal("group-accessible setup socket was accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o710); err != nil {
		t.Fatal(err)
	}
	if err := validateActivatedUnixListener(listener, path); err == nil {
		t.Fatal("group-accessible setup socket directory was accepted")
	}
}
