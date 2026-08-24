//go:build linux

package setup

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

func validateActivatedUnixListener(listener net.Listener, expectedPath string) error {
	if listener == nil || listener.Addr() == nil || listener.Addr().Network() != "unix" || listener.Addr().String() != expectedPath {
		return errors.New("setup listener must be the fixed systemd Unix socket")
	}
	if _, ok := listener.(*net.UnixListener); !ok {
		return errors.New("setup listener must be a Unix stream listener")
	}
	info, err := os.Lstat(expectedPath)
	if err != nil {
		return fmt.Errorf("inspect setup Unix socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return errors.New("setup Unix socket must be a root-only mode-0600 socket")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || stat.Gid != 0 {
		return errors.New("setup Unix socket must be owned by root:root")
	}
	directoryInfo, err := os.Lstat(filepath.Dir(expectedPath))
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 || directoryInfo.Mode().Perm() != 0o700 {
		return errors.New("setup Unix socket directory must be a root-only mode-0700 directory")
	}
	directoryStat, ok := directoryInfo.Sys().(*syscall.Stat_t)
	if !ok || directoryStat.Uid != 0 || directoryStat.Gid != 0 {
		return errors.New("setup Unix socket directory must be owned by root:root")
	}
	return nil
}

func authorizedRootUnixConnection(connection net.Conn, expectedPath string) bool {
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok || unixConnection.LocalAddr() == nil || unixConnection.LocalAddr().Network() != "unix" || unixConnection.LocalAddr().String() != expectedPath {
		return false
	}
	raw, err := unixConnection.SyscallConn()
	if err != nil {
		return false
	}
	var credentials *syscall.Ucred
	var controlErr error
	if err := raw.Control(func(fileDescriptor uintptr) {
		credentials, controlErr = syscall.GetsockoptUcred(int(fileDescriptor), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || controlErr != nil || credentials == nil {
		return false
	}
	return credentials.Uid == 0
}
