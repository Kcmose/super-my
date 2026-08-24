package setupipc

import "errors"

var (
	// ErrConflict means an IPC request or result already exists. Existing
	// entries are never overwritten or removed by the losing caller.
	ErrConflict = errors.New("setup finalization IPC is already in use")
	// ErrTimeout means the privileged finalizer did not publish a valid result
	// before the caller's deadline or the broker's hard deadline.
	ErrTimeout = errors.New("setup finalization IPC timed out")
	// ErrCanceled means finalization was canceled before a result was accepted.
	ErrCanceled = errors.New("setup finalization IPC was canceled")
	// ErrProtocol covers malformed requests/results and insecure filesystem
	// metadata. Its deliberately fixed text cannot disclose request contents.
	ErrProtocol = errors.New("setup finalization IPC protocol violation")
	// ErrUnavailable covers failures to access or durably update the private IPC
	// directory. Its deliberately fixed text cannot disclose request contents.
	ErrUnavailable = errors.New("setup finalization IPC is unavailable")
	// ErrNotReady is returned when a root oneshot attempts to consume a request
	// that has not been published yet.
	ErrNotReady = errors.New("setup finalization request is not ready")
	// ErrInvalidResult means a root oneshot attempted to publish a result that
	// is outside the closed result schema.
	ErrInvalidResult = errors.New("setup finalization result is invalid")
)

// FinalizeError reports a stable machine-readable failure code without ever
// rendering it in Error(). This prevents a compromised or buggy oneshot from
// reflecting result contents into logs that print the error.
type FinalizeError struct {
	Code string
}

func (*FinalizeError) Error() string {
	return "privileged setup finalization failed"
}
