package auth

import "testing"

func TestSessionAndCSRFTokens(t *testing.T) {
	id, plaintext, hash, err := NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken() error = %v", err)
	}
	parsedID, ok := ParseSessionToken(plaintext)
	if !ok || parsedID != id {
		t.Fatalf("ParseSessionToken() = %q, %v", parsedID, ok)
	}
	if plaintext == hash || !ConstantTimeHashEqual(hash, plaintext) || ConstantTimeHashEqual(hash, plaintext+"x") {
		t.Fatal("session token hashing did not preserve opaque-token semantics")
	}
	for _, invalid := range []string{"", plaintext + "=", "session.v1.bad.value", " session.v1.bad.value"} {
		if _, ok := ParseSessionToken(invalid); ok {
			t.Fatalf("ParseSessionToken(%q) accepted invalid token", invalid)
		}
	}

	csrf, csrfHash, err := DeriveCSRFToken(plaintext)
	if err != nil {
		t.Fatalf("DeriveCSRFToken() error = %v", err)
	}
	csrfAgain, csrfHashAgain, err := DeriveCSRFToken(plaintext)
	if err != nil {
		t.Fatalf("DeriveCSRFToken(repeat) error = %v", err)
	}
	if !ParseCSRFToken(csrf) || csrf == plaintext || csrf == csrfHash || !ConstantTimeHashEqual(csrfHash, csrf) {
		t.Fatal("CSRF token did not have the required session-bound/hash representation")
	}
	if csrfAgain != csrf || csrfHashAgain != csrfHash {
		t.Fatal("CSRF derivation was not stable for one session")
	}
	_, otherSession, _, err := NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken(other) error = %v", err)
	}
	otherCSRF, _, err := DeriveCSRFToken(otherSession)
	if err != nil {
		t.Fatalf("DeriveCSRFToken(other) error = %v", err)
	}
	if otherCSRF == csrf {
		t.Fatal("different sessions derived the same CSRF token")
	}
	if _, _, err := DeriveCSRFToken("not-a-session-token"); err == nil {
		t.Fatal("DeriveCSRFToken() accepted an invalid session token")
	}
	if ParseCSRFToken(csrf + "=") {
		t.Fatal("ParseCSRFToken() accepted non-canonical encoding")
	}
}
