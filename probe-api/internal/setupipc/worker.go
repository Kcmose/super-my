package setupipc

import (
	"errors"

	"probe-api/internal/setup"
)

const (
	requestFileName = "finalize.json"
	resultFileName  = "result.json"
)

// ReadRequest consumes one root-owned 0600 request and validates it through
// setup.DecodeCompleteRequest. The caller owns the returned plaintext and must
// defer request.ClearSecrets(). The file and its plaintext bytes are wiped and
// unlinked before this function returns.
func ReadRequest(path string) (setup.CompleteRequest, error) {
	return readRequestWithOwner(path, 0)
}

func readRequestWithOwner(path string, ownerUID uint32) (setup.CompleteRequest, error) {
	directoryPath, name, err := splitSecurePath(path)
	if err != nil {
		return setup.CompleteRequest{}, ErrUnavailable
	}
	directory, err := openSecureDirectory(directoryPath, ownerUID)
	if err != nil {
		return setup.CompleteRequest{}, ErrUnavailable
	}
	defer directory.close()

	contents, err := directory.readAndRemove(name, maxRequestBytes)
	if err != nil {
		if errors.Is(err, errSecureNotFound) {
			return setup.CompleteRequest{}, ErrNotReady
		}
		return setup.CompleteRequest{}, ErrProtocol
	}
	defer clear(contents)
	request, err := setup.DecodeCompleteRequest(contents)
	if err != nil {
		return setup.CompleteRequest{}, ErrProtocol
	}
	return request, nil
}

// WriteResult atomically creates one root-owned 0600 result. It never
// overwrites an existing entry and only accepts the closed Result schema.
func WriteResult(path string, result Result) error {
	return writeResultWithOwner(path, result, 0)
}

func writeResultWithOwner(path string, result Result, ownerUID uint32) error {
	contents, err := encodeResult(result)
	if err != nil {
		return ErrInvalidResult
	}
	defer clear(contents)

	directoryPath, name, err := splitSecurePath(path)
	if err != nil {
		return ErrUnavailable
	}
	directory, err := openSecureDirectory(directoryPath, ownerUID)
	if err != nil {
		return ErrUnavailable
	}
	defer directory.close()
	if err := directory.createExclusive(name, contents); err != nil {
		if errors.Is(err, ErrConflict) {
			return ErrConflict
		}
		return ErrUnavailable
	}
	return nil
}
