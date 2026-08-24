package setupipc

import (
	"context"
	"errors"
	"time"

	"probe-api/internal/setup"
)

const (
	DefaultDirectory   = "/run/probe-panel-setup"
	DefaultRequestPath = DefaultDirectory + "/finalize.json"
	DefaultResultPath  = DefaultDirectory + "/result.json"
	DefaultMaximumWait = 30 * time.Minute

	defaultPollInterval = 100 * time.Millisecond
)

// Broker is the unprivileged setup server's narrow handoff to the privileged
// root oneshot. Production construction is deliberately not configurable: the
// directory, file names, owner, and maximum wait are fixed.
type Broker struct {
	directory    string
	ownerUID     uint32
	pollInterval time.Duration
	maximumWait  time.Duration
}

var _ setup.Finalizer = (*Broker)(nil)

func NewBroker() *Broker {
	return &Broker{
		directory:    DefaultDirectory,
		ownerUID:     0,
		pollInterval: defaultPollInterval,
		maximumWait:  DefaultMaximumWait,
	}
}

// newBrokerForTesting is intentionally unexported so production callers
// cannot weaken the fixed root ownership or fixed runtime path.
func newBrokerForTesting(directory string, ownerUID uint32, pollInterval, maximumWait time.Duration) *Broker {
	return &Broker{
		directory:    directory,
		ownerUID:     ownerUID,
		pollInterval: pollInterval,
		maximumWait:  maximumWait,
	}
}

func (broker *Broker) Finalize(ctx context.Context, request setup.CompleteRequest) error {
	if broker == nil || ctx == nil || broker.directory == "" || broker.pollInterval <= 0 || broker.maximumWait <= 0 {
		return ErrUnavailable
	}
	select {
	case <-ctx.Done():
		return cancellationError(ctx.Err())
	default:
	}

	requestCopy := request.Clone()
	defer requestCopy.ClearSecrets()
	payload, err := encodeCompleteRequest(requestCopy)
	if err != nil {
		return ErrProtocol
	}

	directory, err := openSecureDirectory(broker.directory, broker.ownerUID)
	if err != nil {
		clear(payload)
		return ErrUnavailable
	}
	defer directory.close()

	if err := directory.ensureAbsent(requestFileName); err != nil {
		clear(payload)
		if errors.Is(err, ErrConflict) {
			return ErrConflict
		}
		return ErrUnavailable
	}
	if err := directory.ensureAbsent(resultFileName); err != nil {
		clear(payload)
		if errors.Is(err, ErrConflict) {
			return ErrConflict
		}
		return ErrUnavailable
	}
	if err := directory.createExclusive(requestFileName, payload); err != nil {
		clear(payload)
		if errors.Is(err, ErrConflict) {
			return ErrConflict
		}
		return ErrUnavailable
	}
	clear(payload)

	// Publication transfers request ownership to the root path-triggered
	// worker. In particular, cancellation or timeout must not race that worker
	// by unlinking finalize.json before ReadRequest can atomically consume it.
	// The worker owns the request and the broker consumes a result only when it
	// is actually observed.

	waitContext, cancel := context.WithTimeout(ctx, broker.maximumWait)
	defer cancel()
	ticker := time.NewTicker(broker.pollInterval)
	defer ticker.Stop()

	for {
		contents, err := directory.readAndRemove(resultFileName, maxResultBytes)
		if err == nil {
			result, decodeErr := decodeResult(contents)
			clear(contents)
			if decodeErr != nil {
				return ErrProtocol
			}
			if result.Success {
				return nil
			}
			return &FinalizeError{Code: result.ErrorCode}
		}
		if !errors.Is(err, errSecureNotFound) {
			return ErrProtocol
		}

		select {
		case <-waitContext.Done():
			return cancellationError(waitContext.Err())
		case <-ticker.C:
		}
	}
}

func cancellationError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	return ErrCanceled
}
