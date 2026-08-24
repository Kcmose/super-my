//go:build linux

package setupipc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"probe-api/internal/setup"
)

const testWait = 3 * time.Second

func TestBrokerImplementsFinalizerAndUsesFixedProductionPaths(t *testing.T) {
	var _ setup.Finalizer = NewBroker()
	broker := NewBroker()
	if broker.directory != DefaultDirectory || filepath.Join(broker.directory, requestFileName) != DefaultRequestPath || filepath.Join(broker.directory, resultFileName) != DefaultResultPath {
		t.Fatalf("production broker paths are not fixed: %#v", broker)
	}
	if broker.ownerUID != 0 {
		t.Fatalf("production broker owner = %d, want root", broker.ownerUID)
	}
	if broker.maximumWait != DefaultMaximumWait {
		t.Fatalf("production broker wait = %s, want %s", broker.maximumWait, DefaultMaximumWait)
	}
}

func TestBrokerTransfersPlaintextRequestAndConsumesExchange(t *testing.T) {
	directory, ownerUID := privateTestDirectory(t)
	broker := testBroker(directory, ownerUID)
	request := validRequest()
	databaseSecret := append([]byte(nil), request.Database.Password...)
	administratorSecret := append([]byte(nil), request.Administrator.Password...)
	defer clear(databaseSecret)
	defer clear(administratorSecret)

	resultChannel := make(chan error, 1)
	go func() { resultChannel <- broker.Finalize(context.Background(), request) }()

	requestPath := filepath.Join(directory, requestFileName)
	waitForFile(t, requestPath)
	raw, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatalf("read published request: %v", err)
	}
	defer clear(raw)
	if !bytes.Contains(raw, databaseSecret) || !bytes.Contains(raw, administratorSecret) {
		t.Fatalf("published request does not contain actual passwords: %s", raw)
	}
	if bytes.Contains(raw, []byte("[REDACTED]")) {
		t.Fatalf("published request used Secret.MarshalJSON: %s", raw)
	}
	assertSecureFile(t, requestPath, ownerUID)

	consumed, err := readRequestWithOwner(requestPath, ownerUID)
	if err != nil {
		t.Fatalf("consume request: %v", err)
	}
	if !bytes.Equal(consumed.Database.Password, databaseSecret) || !bytes.Equal(consumed.Database.PasswordConfirmation, databaseSecret) ||
		!bytes.Equal(consumed.Administrator.Password, administratorSecret) || !bytes.Equal(consumed.Administrator.PasswordConfirmation, administratorSecret) {
		consumed.ClearSecrets()
		t.Fatal("consumed request passwords do not match")
	}
	if got := consumed.Allowlist[0]; got != "192.0.2.44/32" {
		consumed.ClearSecrets()
		t.Fatalf("normalized allowlist = %q", got)
	}
	databaseBacking := consumed.Database.Password
	administratorBacking := consumed.Administrator.Password
	consumed.ClearSecrets()
	assertZeroed(t, databaseBacking)
	assertZeroed(t, administratorBacking)
	assertNotExists(t, requestPath)

	resultPath := filepath.Join(directory, resultFileName)
	if err := writeResultWithOwner(resultPath, Result{Success: true}, ownerUID); err != nil {
		t.Fatalf("write success result: %v", err)
	}
	if err := receive(t, resultChannel); err != nil {
		t.Fatalf("broker finalization: %v", err)
	}
	assertNotExists(t, requestPath)
	assertNotExists(t, resultPath)

	// Broker clones and clears only its private copy; the server remains
	// responsible for clearing the request it passed by value.
	if !bytes.Equal(request.Database.Password, databaseSecret) || !bytes.Equal(request.Administrator.Password, administratorSecret) {
		t.Fatal("broker altered caller-owned secrets")
	}
	request.ClearSecrets()
}

func TestBrokerReturnsOpaqueStableFailure(t *testing.T) {
	directory, ownerUID := privateTestDirectory(t)
	broker := testBroker(directory, ownerUID)
	request := validRequest()
	secret := append([]byte(nil), request.Administrator.Password...)
	defer clear(secret)
	defer request.ClearSecrets()

	resultChannel := make(chan error, 1)
	go func() { resultChannel <- broker.Finalize(context.Background(), request) }()
	requestPath := filepath.Join(directory, requestFileName)
	waitForFile(t, requestPath)
	consumed, err := readRequestWithOwner(requestPath, ownerUID)
	if err != nil {
		t.Fatalf("consume request: %v", err)
	}
	consumed.ClearSecrets()
	if err := writeResultWithOwner(filepath.Join(directory, resultFileName), Result{
		Success: false, ErrorCode: ErrorCodeDatabaseMigrationFailed,
	}, ownerUID); err != nil {
		t.Fatalf("write failed result: %v", err)
	}

	err = receive(t, resultChannel)
	var finalizeError *FinalizeError
	if !errors.As(err, &finalizeError) || finalizeError.Code != ErrorCodeDatabaseMigrationFailed {
		t.Fatalf("broker error = %#v", err)
	}
	if strings.Contains(err.Error(), finalizeError.Code) || bytes.Contains([]byte(err.Error()), secret) {
		t.Fatalf("rendered error disclosed result/request contents: %q", err)
	}
}

func TestBrokerRejectsSecretInMaliciousResultWithoutReflectingIt(t *testing.T) {
	directory, ownerUID := privateTestDirectory(t)
	broker := testBroker(directory, ownerUID)
	request := validRequest()
	secret := append([]byte(nil), request.Administrator.Password...)
	defer clear(secret)
	defer request.ClearSecrets()

	resultChannel := make(chan error, 1)
	go func() { resultChannel <- broker.Finalize(context.Background(), request) }()
	waitForFile(t, filepath.Join(directory, requestFileName))
	consumed, err := readRequestWithOwner(filepath.Join(directory, requestFileName), ownerUID)
	if err != nil {
		t.Fatalf("consume request: %v", err)
	}
	consumed.ClearSecrets()

	malicious := []byte(`{"success":false,"error_code":`)
	malicious, err = appendJSONString(malicious, secret)
	if err != nil {
		t.Fatalf("quote malicious result: %v", err)
	}
	malicious = append(malicious, '}')
	dir, err := openSecureDirectory(directory, ownerUID)
	if err != nil {
		clear(malicious)
		t.Fatalf("open IPC directory: %v", err)
	}
	if err := dir.createExclusive(resultFileName, malicious); err != nil {
		dir.close()
		clear(malicious)
		t.Fatalf("publish malicious result: %v", err)
	}
	dir.close()
	clear(malicious)

	err = receive(t, resultChannel)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("broker error = %v, want ErrProtocol", err)
	}
	if bytes.Contains([]byte(err.Error()), secret) {
		t.Fatalf("broker reflected password in error: %q", err)
	}
	assertNotExists(t, filepath.Join(directory, resultFileName))
}

func TestBrokerTimeoutAndCancellationPreservePublishedRequestForRootWorker(t *testing.T) {
	t.Run("hard timeout", func(t *testing.T) {
		directory, ownerUID := privateTestDirectory(t)
		broker := newBrokerForTesting(directory, ownerUID, 2*time.Millisecond, 30*time.Millisecond)
		request := validRequest()
		defer request.ClearSecrets()
		err := broker.Finalize(context.Background(), request)
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("Finalize() error = %v, want ErrTimeout", err)
		}
		requestPath := filepath.Join(directory, requestFileName)
		assertSecureFile(t, requestPath, ownerUID)
		assertNotExists(t, filepath.Join(directory, resultFileName))
		consumed, consumeErr := readRequestWithOwner(requestPath, ownerUID)
		if consumeErr != nil {
			t.Fatalf("root worker could not consume timed-out request: %v", consumeErr)
		}
		consumed.ClearSecrets()
		assertNotExists(t, requestPath)
	})

	t.Run("already canceled", func(t *testing.T) {
		directory, ownerUID := privateTestDirectory(t)
		broker := testBroker(directory, ownerUID)
		request := validRequest()
		defer request.ClearSecrets()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := broker.Finalize(ctx, request); !errors.Is(err, ErrCanceled) {
			t.Fatalf("Finalize() error = %v, want ErrCanceled", err)
		}
		assertNotExists(t, filepath.Join(directory, requestFileName))
	})

	t.Run("canceled after publish", func(t *testing.T) {
		directory, ownerUID := privateTestDirectory(t)
		broker := testBroker(directory, ownerUID)
		request := validRequest()
		defer request.ClearSecrets()
		ctx, cancel := context.WithCancel(context.Background())
		resultChannel := make(chan error, 1)
		go func() { resultChannel <- broker.Finalize(ctx, request) }()
		waitForFile(t, filepath.Join(directory, requestFileName))
		cancel()
		if err := receive(t, resultChannel); !errors.Is(err, ErrCanceled) {
			t.Fatalf("Finalize() error = %v, want ErrCanceled", err)
		}
		requestPath := filepath.Join(directory, requestFileName)
		assertSecureFile(t, requestPath, ownerUID)
		consumed, err := readRequestWithOwner(requestPath, ownerUID)
		if err != nil {
			t.Fatalf("root worker could not consume canceled request: %v", err)
		}
		consumed.ClearSecrets()
		assertNotExists(t, requestPath)
	})
}

func TestBrokerFailsClosedOnPreexistingEntriesWithoutRemovingThem(t *testing.T) {
	for _, name := range []string{requestFileName, resultFileName} {
		t.Run(name, func(t *testing.T) {
			directory, ownerUID := privateTestDirectory(t)
			dir, err := openSecureDirectory(directory, ownerUID)
			if err != nil {
				t.Fatal(err)
			}
			preexisting := []byte(`{"preexisting":true}`)
			if err := dir.createExclusive(name, preexisting); err != nil {
				dir.close()
				t.Fatal(err)
			}
			clear(preexisting)
			dir.close()

			request := validRequest()
			secret := append([]byte(nil), request.Administrator.Password...)
			err = testBroker(directory, ownerUID).Finalize(context.Background(), request)
			request.ClearSecrets()
			if !errors.Is(err, ErrConflict) {
				clear(secret)
				t.Fatalf("Finalize() error = %v, want ErrConflict", err)
			}
			if bytes.Contains([]byte(err.Error()), secret) {
				clear(secret)
				t.Fatalf("conflict error leaked password: %q", err)
			}
			clear(secret)
			if _, statErr := os.Lstat(filepath.Join(directory, name)); statErr != nil {
				t.Fatalf("losing broker removed preexisting entry: %v", statErr)
			}
			dir, err = openSecureDirectory(directory, ownerUID)
			if err != nil {
				t.Fatal(err)
			}
			if err := dir.removeIfExists(name, maxRequestBytes); err != nil {
				dir.close()
				t.Fatal(err)
			}
			dir.close()
		})
	}
}

func TestReadRequestStrictSchemaAndOneTimeConsumption(t *testing.T) {
	directory, ownerUID := privateTestDirectory(t)
	path := filepath.Join(directory, requestFileName)
	request := validRequest()
	validJSON, err := encodeCompleteRequest(request)
	request.ClearSecrets()
	if err != nil {
		t.Fatal(err)
	}
	malformed := append([]byte(nil), validJSON[:len(validJSON)-1]...)
	malformed = append(malformed, `,"unexpected":true}`...)
	secret := []byte("Admin-Strong-Secret-5678")
	defer clear(secret)
	if !bytes.Contains(malformed, secret) {
		t.Fatal("test payload lost its secret")
	}
	clear(validJSON)

	dir, err := openSecureDirectory(directory, ownerUID)
	if err != nil {
		clear(malformed)
		t.Fatal(err)
	}
	if err := dir.createExclusive(requestFileName, malformed); err != nil {
		dir.close()
		clear(malformed)
		t.Fatal(err)
	}
	dir.close()
	clear(malformed)

	if _, err := readRequestWithOwner(path, ownerUID); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ReadRequest() error = %v, want ErrProtocol", err)
	} else if bytes.Contains([]byte(err.Error()), secret) {
		t.Fatalf("ReadRequest() error leaked password: %q", err)
	}
	assertNotExists(t, path)
	if _, err := readRequestWithOwner(path, ownerUID); !errors.Is(err, ErrNotReady) {
		t.Fatalf("second ReadRequest() error = %v, want ErrNotReady", err)
	}
}

func TestRequestCodecRoundTripEscapesSecretsWithoutRedaction(t *testing.T) {
	request := validRequest()
	request.Database.Password = setup.Secret("Database-\"\\-密碼-1234")
	request.Database.PasswordConfirmation = append(setup.Secret(nil), request.Database.Password...)
	request.Administrator.Password = setup.Secret("Admin-\n\t\"\\-密碼-5678")
	request.Administrator.PasswordConfirmation = append(setup.Secret(nil), request.Administrator.Password...)
	defer request.ClearSecrets()

	encoded, err := encodeCompleteRequest(request)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	if bytes.Contains(encoded, []byte("[REDACTED]")) {
		clear(encoded)
		t.Fatal("codec emitted REDACTED")
	}
	decoded, err := setup.DecodeCompleteRequest(encoded)
	clear(encoded)
	if err != nil {
		t.Fatalf("decode request: %v", err)
	}
	defer decoded.ClearSecrets()
	if !bytes.Equal(decoded.Database.Password, request.Database.Password) || !bytes.Equal(decoded.Administrator.Password, request.Administrator.Password) {
		t.Fatal("escaped secret did not round trip")
	}
}

func TestRequestCodecRoundTripsPrivateCAIPv6Ingress(t *testing.T) {
	request := validRequest()
	request.Domains = setup.DomainInput{}
	request.Network = setup.NetworkInput{Address: "2001:db8::25"}
	request.TLS = setup.TLSInput{Mode: "private_ca", Email: ""}
	defer request.ClearSecrets()

	encoded, err := encodeCompleteRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := setup.DecodeCompleteRequest(encoded)
	clear(encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer decoded.ClearSecrets()
	access, err := decoded.AccessConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Network.Address != "2001:db8::25" || access.AgentOrigin != "https://[2001:db8::25]:18454" {
		t.Fatalf("decoded IP access = %#v", access)
	}
}

func TestResultCodecIsStrictAndNeverAcceptsArbitrarySecret(t *testing.T) {
	secret := "A-Unique-Administrator-Password-5678"
	tests := []string{
		`{}`,
		`{"success":true,"success":true}`,
		`{"success":true,"error_code":"internal_error"}`,
		`{"success":false}`,
		`{"success":false,"error_code":"` + secret + `"}`,
		`{"success":false,"error_code":"internal_error","extra":true}`,
		`{"success":"true"}`,
		`{"success":true} trailing`,
	}
	for _, input := range tests {
		if _, err := decodeResult([]byte(input)); !errors.Is(err, ErrProtocol) {
			t.Errorf("decodeResult(%q) error = %v, want ErrProtocol", input, err)
		} else if strings.Contains(err.Error(), secret) {
			t.Errorf("decodeResult(%q) leaked secret: %v", input, err)
		}
	}

	if _, err := encodeResult(Result{Success: false, ErrorCode: secret}); !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("encodeResult(secret) error = %v, want ErrInvalidResult", err)
	} else if strings.Contains(err.Error(), secret) {
		t.Fatalf("encodeResult(secret) leaked secret: %v", err)
	}
	success, err := encodeResult(Result{Success: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(success), secret) || string(success) != `{"success":true}` {
		clear(success)
		t.Fatalf("success JSON = %s", success)
	}
	clear(success)
	failure, err := encodeResult(Result{Success: false, ErrorCode: ErrorCodeInternal})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(failure), secret) || string(failure) != `{"success":false,"error_code":"internal_error"}` {
		clear(failure)
		t.Fatalf("failure JSON = %s", failure)
	}
	clear(failure)
}

func TestWriteResultIsAtomicExclusiveAndSecure(t *testing.T) {
	directory, ownerUID := privateTestDirectory(t)
	path := filepath.Join(directory, resultFileName)
	if err := writeResultWithOwner(path, Result{Success: true}, ownerUID); err != nil {
		t.Fatalf("first WriteResult: %v", err)
	}
	assertSecureFile(t, path, ownerUID)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(before)
	if err := writeResultWithOwner(path, Result{Success: false, ErrorCode: ErrorCodeInternal}, ownerUID); !errors.Is(err, ErrConflict) {
		t.Fatalf("second WriteResult error = %v, want ErrConflict", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(after)
	if !bytes.Equal(before, after) {
		t.Fatal("conflicting WriteResult overwrote existing result")
	}
}

func TestConcurrentWriteResultAllowsExactlyOnePublisher(t *testing.T) {
	directory, ownerUID := privateTestDirectory(t)
	path := filepath.Join(directory, resultFileName)
	start := make(chan struct{})
	errorsChannel := make(chan error, 2)
	var waitGroup sync.WaitGroup
	for index := 0; index < 2; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			errorsChannel <- writeResultWithOwner(path, Result{Success: true}, ownerUID)
		}()
	}
	close(start)
	waitGroup.Wait()
	close(errorsChannel)
	var successes, conflicts int
	for err := range errorsChannel {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected publisher error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d, want 1/1", successes, conflicts)
	}
}

func TestRejectsBroadOrSymlinkedParentDirectory(t *testing.T) {
	t.Run("broad permissions", func(t *testing.T) {
		directory, ownerUID := privateTestDirectory(t)
		if err := os.Chmod(directory, 0o750); err != nil {
			t.Fatal(err)
		}
		request := validRequest()
		defer request.ClearSecrets()
		if err := testBroker(directory, ownerUID).Finalize(context.Background(), request); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Finalize() error = %v, want ErrUnavailable", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		realDirectory := filepath.Join(root, "real")
		if err := os.Mkdir(realDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		linkDirectory := filepath.Join(root, "link")
		if err := os.Symlink(realDirectory, linkDirectory); err != nil {
			t.Fatal(err)
		}
		request := validRequest()
		defer request.ClearSecrets()
		if err := testBroker(linkDirectory, uint32(os.Geteuid())).Finalize(context.Background(), request); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Finalize() error = %v, want ErrUnavailable", err)
		}
		assertNotExists(t, filepath.Join(realDirectory, requestFileName))
	})
}

func TestReadRequestRejectsUnsafeFileMetadataWithoutFollowingOrDeleting(t *testing.T) {
	request := validRequest()
	encoded, err := encodeCompleteRequest(request)
	request.ClearSecrets()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)

	t.Run("permissions", func(t *testing.T) {
		directory, ownerUID := privateTestDirectory(t)
		path := filepath.Join(directory, requestFileName)
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		// The release builder deliberately runs with umask 077. Chmod after
		// creation so this test still creates the intended unsafe metadata.
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readRequestWithOwner(path, ownerUID); !errors.Is(err, ErrProtocol) {
			t.Fatalf("ReadRequest() error = %v, want ErrProtocol", err)
		}
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("unsafe file was removed: %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		directory, ownerUID := privateTestDirectory(t)
		target := filepath.Join(directory, "target.json")
		if err := os.WriteFile(target, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, requestFileName)
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if _, err := readRequestWithOwner(path, ownerUID); !errors.Is(err, ErrProtocol) {
			t.Fatalf("ReadRequest() error = %v, want ErrProtocol", err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("symlink target was removed: %v", err)
		}
	})

	t.Run("hard link", func(t *testing.T) {
		directory, ownerUID := privateTestDirectory(t)
		target := filepath.Join(directory, "target.json")
		if err := os.WriteFile(target, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, requestFileName)
		if err := os.Link(target, path); err != nil {
			t.Fatal(err)
		}
		if _, err := readRequestWithOwner(path, ownerUID); !errors.Is(err, ErrProtocol) {
			t.Fatalf("ReadRequest() error = %v, want ErrProtocol", err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("hard-link target was removed: %v", err)
		}
	})

	t.Run("owner", func(t *testing.T) {
		if os.Geteuid() != 0 {
			t.Skip("owner mismatch fixture requires root")
		}
		directory, ownerUID := privateTestDirectory(t)
		path := filepath.Join(directory, requestFileName)
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, 65534, -1); err != nil {
			t.Fatal(err)
		}
		if _, err := readRequestWithOwner(path, ownerUID); !errors.Is(err, ErrProtocol) {
			t.Fatalf("ReadRequest() error = %v, want ErrProtocol", err)
		}
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("wrong-owner file was removed: %v", err)
		}
	})
}

func TestReadRequestUsesFakeOwnerPolicy(t *testing.T) {
	directory, ownerUID := privateTestDirectory(t)
	request := validRequest()
	encoded, err := encodeCompleteRequest(request)
	request.ClearSecrets()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	dir, err := openSecureDirectory(directory, ownerUID)
	if err != nil {
		t.Fatal(err)
	}
	if err := dir.createExclusive(requestFileName, encoded); err != nil {
		dir.close()
		t.Fatal(err)
	}
	dir.close()
	if _, err := readRequestWithOwner(filepath.Join(directory, requestFileName), ownerUID+1); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("fake owner mismatch error = %v, want ErrUnavailable", err)
	}
}

func validRequest() setup.CompleteRequest {
	databasePassword := setup.Secret("Database-Strong-Secret-1234")
	administratorPassword := setup.Secret("Admin-Strong-Secret-5678")
	return setup.CompleteRequest{
		Database: setup.DatabaseInput{
			Mode:                 "local",
			Name:                 "probe_panel",
			Username:             "probe_panel_user",
			Password:             append(setup.Secret(nil), databasePassword...),
			PasswordConfirmation: append(setup.Secret(nil), databasePassword...),
		},
		Domains: setup.DomainInput{
			Panel: "panel.monitor.test",
			Admin: "admin.monitor.test",
			Agent: "api.monitor.test",
		},
		Network:   setup.NetworkInput{Address: ""},
		TLS:       setup.TLSInput{Mode: "acme", Email: "operator@monitor.test"},
		Allowlist: []string{"192.0.2.44", "2001:db8:1234::/48"},
		Administrator: setup.AdministratorInput{
			Username:             "administrator",
			Password:             append(setup.Secret(nil), administratorPassword...),
			PasswordConfirmation: append(setup.Secret(nil), administratorPassword...),
		},
	}
}

func privateTestDirectory(t *testing.T) (string, uint32) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatalf("chmod test directory: %v", err)
	}
	return directory, uint32(os.Geteuid())
}

func testBroker(directory string, ownerUID uint32) *Broker {
	return newBrokerForTesting(directory, ownerUID, 2*time.Millisecond, testWait)
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect %s: %v", path, err)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func receive(t *testing.T, channel <-chan error) error {
	t.Helper()
	select {
	case err := <-channel:
		return err
	case <-time.After(testWait):
		t.Fatal("timed out waiting for broker")
		return nil
	}
}

func assertNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s still exists or cannot be inspected: %v", path, err)
	}
}

func assertSecureFile(t *testing.T, path string, ownerUID uint32) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect secure file: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("secure file mode = %v", info.Mode())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != ownerUID || stat.Nlink != 1 {
		t.Fatalf("secure file metadata = %#v, want uid=%d nlink=1", info.Sys(), ownerUID)
	}
}

func assertZeroed(t *testing.T, value []byte) {
	t.Helper()
	if !bytes.Equal(value, make([]byte, len(value))) {
		t.Fatalf("secret backing bytes were not cleared: %s", fmt.Sprintf("%x", value))
	}
}
