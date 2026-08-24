package agent

import "testing"

func TestAgentTokenRoundTripAndHashComparison(t *testing.T) {
	tokenID, plaintext, storedHash, err := NewAgentToken()
	if err != nil {
		t.Fatalf("NewAgentToken() error = %v", err)
	}
	parsedID, ok := ParseAgentToken(plaintext)
	if !ok || parsedID != tokenID {
		t.Fatalf("ParseAgentToken() = %q, %v; want %q, true", parsedID, ok, tokenID)
	}
	if storedHash == plaintext {
		t.Fatal("stored token hash contains plaintext")
	}
	if !ConstantTimeHashEqual(storedHash, plaintext) {
		t.Fatal("ConstantTimeHashEqual() rejected the matching token")
	}
	if ConstantTimeHashEqual(storedHash, plaintext+"x") {
		t.Fatal("ConstantTimeHashEqual() accepted a different token")
	}
	if ConstantTimeHashEqual("invalid", plaintext) {
		t.Fatal("ConstantTimeHashEqual() accepted an invalid stored hash")
	}
}

func TestParseAgentTokenRejectsMalformedValues(t *testing.T) {
	for _, value := range []string{"", "agent.v1.invalid.secret", "agent.v2.00000000-0000-0000-0000-000000000000.secret"} {
		if _, ok := ParseAgentToken(value); ok {
			t.Fatalf("ParseAgentToken(%q) unexpectedly succeeded", value)
		}
	}
}
