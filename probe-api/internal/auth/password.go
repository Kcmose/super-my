package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

const (
	maxPasswordBytes = 1024
	passwordMemoryKB = uint32(64 * 1024)
	passwordTime     = uint32(3)
	passwordThreads  = uint8(2)
	passwordSaltSize = 16
	passwordKeySize  = 32

	maxPasswordHashLength = 512
	maxArgonMemoryKB      = uint32(64 * 1024)
	maxArgonTime          = uint32(4)
	maxArgonThreads       = uint8(4)
	maxArgonSaltSize      = 32
	maxArgonKeySize       = 64
)

type passwordParameters struct {
	memory  uint32
	time    uint32
	threads uint8
	keySize uint32
}

var (
	dummyPasswordOnce sync.Once
	dummyPasswordPHC  string
)

func HashPassword(password string) (string, error) {
	passwordBytes := []byte(password)
	defer clear(passwordBytes)
	return HashPasswordBytes(passwordBytes)
}

func HashPasswordBytes(password []byte) (string, error) {
	if !validNewPasswordBytes(password) {
		return "", errors.New("new password must contain 12 to 1024 valid UTF-8 bytes")
	}
	salt := make([]byte, passwordSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return "", errors.New("generate password salt")
	}
	defer clear(salt)
	parameters := passwordParameters{
		memory: passwordMemoryKB, time: passwordTime, threads: passwordThreads, keySize: passwordKeySize,
	}
	return encodePasswordHash(password, salt, parameters), nil
}

func VerifyPassword(encodedHash, password string) (bool, error) {
	parameters, salt, expected, err := decodePasswordHash(encodedHash)
	if err != nil {
		return false, err
	}
	defer clear(salt)
	defer clear(expected)
	candidate := argon2.IDKey([]byte(password), salt, parameters.time, parameters.memory, parameters.threads, parameters.keySize)
	defer clear(candidate)
	return subtle.ConstantTimeCompare(expected, candidate) == 1, nil
}

func DummyPasswordHash() string {
	dummyPasswordOnce.Do(func() {
		parameters := passwordParameters{
			memory: passwordMemoryKB, time: passwordTime, threads: passwordThreads, keySize: passwordKeySize,
		}
		dummyPasswordPHC = encodePasswordHash([]byte("probe-api-dummy-password"), []byte("probe-dummy-salt"), parameters)
	})
	return dummyPasswordPHC
}

func encodePasswordHash(password, salt []byte, parameters passwordParameters) string {
	digest := argon2.IDKey(password, salt, parameters.time, parameters.memory, parameters.threads, parameters.keySize)
	defer clear(digest)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		parameters.memory,
		parameters.time,
		parameters.threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(digest),
	)
}

func decodePasswordHash(encoded string) (passwordParameters, []byte, []byte, error) {
	if encoded == "" || len(encoded) > maxPasswordHashLength {
		return passwordParameters{}, nil, nil, errors.New("password hash has an invalid length")
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return passwordParameters{}, nil, nil, errors.New("password hash has an invalid format")
	}
	parameterParts := strings.Split(parts[3], ",")
	if len(parameterParts) != 3 || !strings.HasPrefix(parameterParts[0], "m=") || !strings.HasPrefix(parameterParts[1], "t=") || !strings.HasPrefix(parameterParts[2], "p=") {
		return passwordParameters{}, nil, nil, errors.New("password hash parameters are invalid")
	}
	memory, err := strconv.ParseUint(strings.TrimPrefix(parameterParts[0], "m="), 10, 32)
	if err != nil || memory < 8 || memory > uint64(maxArgonMemoryKB) {
		return passwordParameters{}, nil, nil, errors.New("password hash memory cost is invalid")
	}
	timeCost, err := strconv.ParseUint(strings.TrimPrefix(parameterParts[1], "t="), 10, 32)
	if err != nil || timeCost < 1 || timeCost > uint64(maxArgonTime) {
		return passwordParameters{}, nil, nil, errors.New("password hash time cost is invalid")
	}
	threads, err := strconv.ParseUint(strings.TrimPrefix(parameterParts[2], "p="), 10, 8)
	if err != nil || threads < 1 || threads > uint64(maxArgonThreads) || memory < threads*8 {
		return passwordParameters{}, nil, nil, errors.New("password hash parallelism is invalid")
	}
	salt, err := decodeCanonicalBase64(parts[4])
	if err != nil || len(salt) < 16 || len(salt) > maxArgonSaltSize {
		return passwordParameters{}, nil, nil, errors.New("password hash salt is invalid")
	}
	digest, err := decodeCanonicalBase64(parts[5])
	if err != nil || len(digest) < 16 || len(digest) > maxArgonKeySize {
		return passwordParameters{}, nil, nil, errors.New("password hash digest is invalid")
	}
	parameters := passwordParameters{
		memory: uint32(memory), time: uint32(timeCost), threads: uint8(threads), keySize: uint32(len(digest)),
	}
	return parameters, salt, digest, nil
}

func decodeCanonicalBase64(encoded string) ([]byte, error) {
	decoded, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || base64.RawStdEncoding.EncodeToString(decoded) != encoded {
		return nil, errors.New("non-canonical base64")
	}
	return decoded, nil
}
