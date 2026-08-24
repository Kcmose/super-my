//go:build !linux

package setupipc

import "errors"

var (
	errSecureNotFound = errors.New("secure IPC entry not found")
	errSecureInvalid  = errors.New("secure IPC requires Linux")
)

type secureDirectory struct{}

func splitSecurePath(string) (string, string, error) { return "", "", errSecureInvalid }
func openSecureDirectory(string, uint32) (*secureDirectory, error) {
	return nil, errSecureInvalid
}
func (*secureDirectory) close()                               {}
func (*secureDirectory) ensureAbsent(string) error            { return errSecureInvalid }
func (*secureDirectory) createExclusive(string, []byte) error { return errSecureInvalid }
func (*secureDirectory) readAndRemove(string, int64) ([]byte, error) {
	return nil, errSecureInvalid
}
func (*secureDirectory) removeIfExists(string, int64) error { return errSecureInvalid }
