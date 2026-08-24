package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoveryLogDoesNotSerializePanicValue(t *testing.T) {
	const secret = "agent-token-secret-must-not-be-logged"
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := requestIDMiddleware(recoveryMiddleware(logger, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(secret)
	})))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatalf("panic value leaked to logs: %s", logs.String())
	}
	if !strings.Contains(logs.String(), `"panic_recovered":true`) {
		t.Fatalf("sanitized panic marker missing: %s", logs.String())
	}
}
