package agent

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const agentTokenPrefix = "agent.v1"

var dummyTokenDigest = sha256.Sum256([]byte("probe-api-invalid-agent-token"))

func NewAgentToken() (tokenID, plaintext, hash string, err error) {
	tokenID, err = newUUID()
	if err != nil {
		return "", "", "", err
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return "", "", "", errors.New("generate Agent token secret")
	}
	plaintext = agentTokenPrefix + "." + tokenID + "." + base64.RawURLEncoding.EncodeToString(secret)
	hash = HashOpaqueToken(plaintext)
	return tokenID, plaintext, hash, nil
}

func HashOpaqueToken(plaintext string) string {
	digest := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(digest[:])
}

func ParseAgentToken(plaintext string) (string, bool) {
	parts := strings.Split(plaintext, ".")
	if len(parts) != 4 || parts[0] != "agent" || parts[1] != "v1" || !validUUID(parts[2]) {
		return "", false
	}
	secret, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(secret) != 32 {
		return "", false
	}
	return parts[2], true
}

func ConstantTimeHashEqual(storedHex, plaintext string) bool {
	expected := dummyTokenDigest[:]
	decoded, err := hex.DecodeString(storedHex)
	validStoredHash := err == nil && len(decoded) == sha256.Size
	if validStoredHash {
		expected = decoded
	}
	candidate := sha256.Sum256([]byte(plaintext))
	return subtle.ConstantTimeCompare(expected, candidate[:]) == 1 && validStoredHash
}

func newUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", errors.New("generate UUID")
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
