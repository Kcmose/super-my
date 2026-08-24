package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

func DecodeLoginRequest(body []byte) (LoginRequest, error) {
	if len(body) == 0 || !utf8.Valid(body) {
		return LoginRequest{}, errors.New("login request must be valid UTF-8 JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return LoginRequest{}, errors.New("login request must be a JSON object")
	}
	var request LoginRequest
	seen := make(map[string]bool, 2)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return LoginRequest{}, errors.New("login request is invalid JSON")
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return LoginRequest{}, errors.New("login request contains duplicate fields")
		}
		seen[key] = true
		switch key {
		case "username":
			if err := decoder.Decode(&request.Username); err != nil {
				return LoginRequest{}, errors.New("username must be a string")
			}
		case "password":
			if err := decoder.Decode(&request.Password); err != nil {
				return LoginRequest{}, errors.New("password must be a string")
			}
		default:
			return LoginRequest{}, errors.New("login request contains an unknown field")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return LoginRequest{}, errors.New("login request is invalid JSON")
	}
	if !seen["username"] || !seen["password"] {
		return LoginRequest{}, errors.New("login request is missing a required field")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return LoginRequest{}, errors.New("login request contains trailing data")
	}
	if err := request.Validate(); err != nil {
		return LoginRequest{}, err
	}
	return request, nil
}
