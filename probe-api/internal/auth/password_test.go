package auth

import (
	"strings"
	"testing"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if strings.Contains(encoded, "correct horse") || !strings.HasPrefix(encoded, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("HashPassword() returned unexpected PHC value %q", encoded)
	}
	matched, err := VerifyPassword(encoded, "correct horse battery staple")
	if err != nil || !matched {
		t.Fatalf("VerifyPassword(correct) = %v, %v", matched, err)
	}
	matched, err = VerifyPassword(encoded, "wrong")
	if err != nil || matched {
		t.Fatalf("VerifyPassword(wrong) = %v, %v", matched, err)
	}
}

func TestPasswordHashRejectsUnsafeParametersBeforeAllocation(t *testing.T) {
	unsafe := "$argon2id$v=19$m=4294967295,t=3,p=2$cHJvYmUtZHVtbXktc2FsdA$AAAAAAAAAAAAAAAAAAAAAA"
	if _, err := VerifyPassword(unsafe, "password"); err == nil || !strings.Contains(err.Error(), "memory cost") {
		t.Fatalf("VerifyPassword(unsafe) error = %v", err)
	}
	if _, err := VerifyPassword(strings.Repeat("x", maxPasswordHashLength+1), "password"); err == nil {
		t.Fatal("VerifyPassword() accepted an oversized encoded hash")
	}
}

func TestHashPasswordBytesAndInputLimits(t *testing.T) {
	password := []byte("temporary-password")
	encoded, err := HashPasswordBytes(password)
	if err != nil {
		t.Fatalf("HashPasswordBytes() error = %v", err)
	}
	matched, err := VerifyPassword(encoded, string(password))
	if err != nil || !matched {
		t.Fatalf("VerifyPassword() = %v, %v", matched, err)
	}
	if _, err := HashPassword(""); err == nil {
		t.Fatal("HashPassword() accepted an empty password")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("HashPassword() accepted a new password shorter than 12 bytes")
	}
	if _, err := HashPassword(strings.Repeat("p", maxPasswordBytes+1)); err == nil {
		t.Fatal("HashPassword() accepted an oversized password")
	}
}
