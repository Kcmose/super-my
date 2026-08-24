//go:build !linux

package setup

import (
	"errors"
	"net"
)

func validateActivatedUnixListener(net.Listener, string) error {
	return errors.New("setup Unix-socket transport is supported only on Linux")
}

func authorizedRootUnixConnection(net.Conn, string) bool { return false }
