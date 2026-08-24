package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	sessionTokenPrefix = "session.v1"
	csrfTokenDomain    = "probe-api/csrf/v1"
)

var dummyOpaqueDigest = sha256.Sum256([]byte("probe-api-invalid-session-token"))

func NewSessionToken() (tokenID, plaintext, hash string, err error) {
	tokenID, err = newUUID()
	if err != nil {
		return "", "", "", err
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return "", "", "", errors.New("generate session token secret")
	}
	plaintext = sessionTokenPrefix + "." + tokenID + "." + base64.RawURLEncoding.EncodeToString(secret)
	clear(secret)
	return tokenID, plaintext, HashOpaqueToken(plaintext), nil
}

func DeriveCSRFToken(sessionToken string) (plaintext, hash string, err error) {
	if _, valid := ParseSessionToken(sessionToken); !valid {
		return "", "", errors.New("derive CSRF token from invalid session token")
	}
	mac := hmac.New(sha256.New, []byte(sessionToken))
	_, _ = mac.Write([]byte(csrfTokenDomain))
	digest := mac.Sum(nil)
	plaintext = base64.RawURLEncoding.EncodeToString(digest)
	clear(digest)
	return plaintext, HashOpaqueToken(plaintext), nil
}

func HashOpaqueToken(plaintext string) string {
	digest := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(digest[:])
}

func ParseSessionToken(plaintext string) (string, bool) {
	parts := strings.Split(plaintext, ".")
	if len(parts) != 4 || parts[0] != "session" || parts[1] != "v1" || !validUUID(parts[2]) {
		return "", false
	}
	secret, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(secret) != 32 || base64.RawURLEncoding.EncodeToString(secret) != parts[3] {
		return "", false
	}
	return parts[2], true
}

func ParseCSRFToken(plaintext string) bool {
	secret, err := base64.RawURLEncoding.DecodeString(plaintext)
	return err == nil && len(secret) == 32 && base64.RawURLEncoding.EncodeToString(secret) == plaintext
}

func ConstantTimeHashEqual(storedHex, plaintext string) bool {
	expected := dummyOpaqueDigest[:]
	decoded, err := hex.DecodeString(storedHex)
	validHash := err == nil && len(decoded) == sha256.Size
	if validHash {
		expected = decoded
	}
	candidate := sha256.Sum256([]byte(plaintext))
	return subtle.ConstantTimeCompare(expected, candidate[:]) == 1 && validHash
}

func ConstantTimeDigestEqual(leftHex, rightHex string) bool {
	left, leftErr := hex.DecodeString(leftHex)
	right, rightErr := hex.DecodeString(rightHex)
	if leftErr != nil || rightErr != nil || len(left) != sha256.Size || len(right) != sha256.Size {
		return false
	}
	return subtle.ConstantTimeCompare(left, right) == 1
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

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		character := value[index]
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return value[14] == '4' && (value[19] == '8' || value[19] == '9' || value[19] == 'a' || value[19] == 'b')
}
