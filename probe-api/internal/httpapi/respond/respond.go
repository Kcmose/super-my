package respond

import (
	"encoding/json"
	"net/http"
)

type ErrorBody struct {
	Error     string `json:"error"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Details   any    `json:"details,omitempty"`
}

func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func Error(w http.ResponseWriter, status int, code, message, requestID string) {
	JSON(w, status, ErrorBody{Error: code, Message: message, RequestID: requestID})
}

func ErrorWithDetails(w http.ResponseWriter, status int, code, message, requestID string, details any) {
	JSON(w, status, ErrorBody{
		Error:     code,
		Message:   message,
		RequestID: requestID,
		Details:   details,
	})
}
