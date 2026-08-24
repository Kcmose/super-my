package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type objectSchema map[string]objectSchema

func decodeStrictObject(data []byte, destination any, schema objectSchema) error {
	if len(data) == 0 {
		return errors.New("request must be a JSON object")
	}
	if err := validateJSONValue(data, schema); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("request contains an invalid field value")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("request contains trailing data")
	}
	return nil
}

func validateJSONValue(data []byte, schema objectSchema) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := validateObjectTokens(decoder, schema); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("request contains trailing data")
	}
	return nil
}

func validateObjectTokens(decoder *json.Decoder, schema objectSchema) error {
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return errors.New("request must be a JSON object")
	}
	seen := make(map[string]struct{}, len(schema))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return errors.New("request contains invalid JSON")
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("request contains invalid JSON")
		}
		child, allowed := schema[key]
		if !allowed {
			return fmt.Errorf("request contains unknown field %q", key)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("request contains duplicate field %q", key)
		}
		seen[key] = struct{}{}
		if child != nil {
			if err := validateObjectTokens(decoder, child); err != nil {
				return err
			}
			continue
		}
		var ignored json.RawMessage
		if err := decoder.Decode(&ignored); err != nil {
			return errors.New("request contains invalid JSON")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return errors.New("request contains invalid JSON")
	}
	return nil
}
