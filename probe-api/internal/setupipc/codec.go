package setupipc

import (
	"bytes"
	"errors"
	"unicode/utf8"

	"probe-api/internal/setup"
)

const (
	maxRequestBytes = int64(64 * 1024)
	maxResultBytes  = int64(1024)
)

// Result is the entire root-oneshot-to-broker response contract. ErrorCode is
// omitted for success and is mandatory for failure.
type Result struct {
	Success   bool
	ErrorCode string
}

// The closed set prevents stderr, database errors, certificate details, or
// submitted secrets from being reflected through result.json.
const (
	ErrorCodePreflightFailed              = "preflight_failed"
	ErrorCodeDatabaseConfigurationFailed  = "database_configuration_failed"
	ErrorCodeDatabaseMigrationFailed      = "database_migration_failed"
	ErrorCodeAdministratorBootstrapFailed = "administrator_bootstrap_failed"
	ErrorCodeTLSProvisioningFailed        = "tls_provisioning_failed"
	ErrorCodeNginxConfigurationFailed     = "nginx_configuration_failed"
	ErrorCodeServiceActivationFailed      = "service_activation_failed"
	ErrorCodeReadinessCheckFailed         = "readiness_check_failed"
	ErrorCodeRollbackFailed               = "rollback_failed"
	ErrorCodeInternal                     = "internal_error"
)

var allowedErrorCodes = map[string]struct{}{
	ErrorCodePreflightFailed:              {},
	ErrorCodeDatabaseConfigurationFailed:  {},
	ErrorCodeDatabaseMigrationFailed:      {},
	ErrorCodeAdministratorBootstrapFailed: {},
	ErrorCodeTLSProvisioningFailed:        {},
	ErrorCodeNginxConfigurationFailed:     {},
	ErrorCodeServiceActivationFailed:      {},
	ErrorCodeReadinessCheckFailed:         {},
	ErrorCodeRollbackFailed:               {},
	ErrorCodeInternal:                     {},
}

// encodeCompleteRequest intentionally does not use json.Marshal on
// setup.CompleteRequest: setup.Secret.MarshalJSON always returns REDACTED.
// Password bytes are escaped directly into the one mutable output buffer so
// that the caller can clear every plaintext JSON byte after publishing it.
func encodeCompleteRequest(request setup.CompleteRequest) ([]byte, error) {
	if err := request.Validate(); err != nil {
		return nil, ErrProtocol
	}
	profile, err := request.EffectiveProfile()
	if err != nil {
		return nil, ErrProtocol
	}

	// The setup validator bounds every field well below 64 KiB. Reserving the
	// entire protocol limit prevents append from abandoning an older backing
	// array that already contains plaintext before the final buffer is cleared.
	encoded := make([]byte, 0, int(maxRequestBytes))
	encoded = append(encoded, `{"profile":`...)
	encoded, err = appendJSONString(encoded, []byte(profile))
	if err != nil {
		clear(encoded)
		return nil, ErrProtocol
	}

	encoded = append(encoded, `,"database":{"mode":`...)
	encoded, err = appendJSONString(encoded, []byte(request.Database.Mode))
	if err != nil {
		clear(encoded)
		return nil, ErrProtocol
	}
	encoded = append(encoded, `,"name":`...)
	encoded, err = appendJSONString(encoded, []byte(request.Database.Name))
	if err != nil {
		clear(encoded)
		return nil, ErrProtocol
	}
	encoded = append(encoded, `,"username":`...)
	encoded, err = appendJSONString(encoded, []byte(request.Database.Username))
	if err != nil {
		clear(encoded)
		return nil, ErrProtocol
	}
	encoded = append(encoded, `,"password":`...)
	encoded, err = appendJSONString(encoded, request.Database.Password)
	if err != nil {
		clear(encoded)
		return nil, ErrProtocol
	}
	encoded = append(encoded, `,"password_confirmation":`...)
	encoded, err = appendJSONString(encoded, request.Database.PasswordConfirmation)
	if err != nil {
		clear(encoded)
		return nil, ErrProtocol
	}

	encoded = append(encoded, `},"domains":{"panel":`...)
	encoded, err = appendJSONString(encoded, []byte(request.Domains.Panel))
	if err != nil {
		clear(encoded)
		return nil, ErrProtocol
	}
	encoded = append(encoded, `,"admin":`...)
	encoded, err = appendJSONString(encoded, []byte(request.Domains.Admin))
	if err != nil {
		clear(encoded)
		return nil, ErrProtocol
	}
	encoded = append(encoded, `,"agent":`...)
	encoded, err = appendJSONString(encoded, []byte(request.Domains.Agent))
	if err != nil {
		clear(encoded)
		return nil, ErrProtocol
	}

	encoded = append(encoded, `},"network":{"address":`...)
	encoded, err = appendJSONString(encoded, []byte(request.Network.Address))
	if err != nil {
		clear(encoded)
		return nil, ErrProtocol
	}

	encoded = append(encoded, `},"tls":{"mode":`...)
	encoded, err = appendJSONString(encoded, []byte(request.TLS.Mode))
	if err != nil {
		clear(encoded)
		return nil, ErrProtocol
	}
	encoded = append(encoded, `,"email":`...)
	encoded, err = appendJSONString(encoded, []byte(request.TLS.Email))
	if err != nil {
		clear(encoded)
		return nil, ErrProtocol
	}

	encoded = append(encoded, `},"allowlist":[`...)
	for index, cidr := range request.Allowlist {
		if index > 0 {
			encoded = append(encoded, ',')
		}
		encoded, err = appendJSONString(encoded, []byte(cidr))
		if err != nil {
			clear(encoded)
			return nil, ErrProtocol
		}
	}

	encoded = append(encoded, `],"administrator":{"username":`...)
	encoded, err = appendJSONString(encoded, []byte(request.Administrator.Username))
	if err != nil {
		clear(encoded)
		return nil, ErrProtocol
	}
	encoded = append(encoded, `,"password":`...)
	encoded, err = appendJSONString(encoded, request.Administrator.Password)
	if err != nil {
		clear(encoded)
		return nil, ErrProtocol
	}
	encoded = append(encoded, `,"password_confirmation":`...)
	encoded, err = appendJSONString(encoded, request.Administrator.PasswordConfirmation)
	if err != nil {
		clear(encoded)
		return nil, ErrProtocol
	}
	encoded = append(encoded, `}}`...)
	if int64(len(encoded)) > maxRequestBytes {
		clear(encoded)
		return nil, ErrProtocol
	}
	return encoded, nil
}

// appendJSONString implements the JSON string escaping needed by this small
// protocol without converting secret []byte values into immutable strings.
func appendJSONString(destination, value []byte) ([]byte, error) {
	if !utf8.Valid(value) {
		return destination, errors.New("invalid UTF-8")
	}
	destination = append(destination, '"')
	for index := 0; index < len(value); {
		character := value[index]
		if character < utf8.RuneSelf {
			switch character {
			case '\\', '"':
				destination = append(destination, '\\', character)
			case '\b':
				destination = append(destination, `\b`...)
			case '\f':
				destination = append(destination, `\f`...)
			case '\n':
				destination = append(destination, `\n`...)
			case '\r':
				destination = append(destination, `\r`...)
			case '\t':
				destination = append(destination, `\t`...)
			default:
				if character < 0x20 {
					const hexadecimal = "0123456789abcdef"
					destination = append(destination, '\\', 'u', '0', '0', hexadecimal[character>>4], hexadecimal[character&0x0f])
				} else {
					destination = append(destination, character)
				}
			}
			index++
			continue
		}
		_, size := utf8.DecodeRune(value[index:])
		destination = append(destination, value[index:index+size]...)
		index += size
	}
	destination = append(destination, '"')
	return destination, nil
}

func encodeResult(result Result) ([]byte, error) {
	if err := validateResult(result); err != nil {
		return nil, err
	}
	if result.Success {
		return []byte(`{"success":true}`), nil
	}
	encoded := append([]byte(`{"success":false,"error_code":`), '"')
	encoded = append(encoded, result.ErrorCode...)
	encoded = append(encoded, '"', '}')
	return encoded, nil
}

func decodeResult(data []byte) (Result, error) {
	// Parse in place so a malicious error_code can never become an immutable
	// Go string containing a submitted password. Only a matched constant is
	// returned to the caller.
	parser := resultJSONParser{data: data}
	result, hasSuccess, hasErrorCode, ok := parser.parse()
	if !ok {
		return Result{}, ErrProtocol
	}
	if !hasSuccess || (result.Success && hasErrorCode) || (!result.Success && !hasErrorCode) {
		return Result{}, ErrProtocol
	}
	if err := validateResult(result); err != nil {
		return Result{}, ErrProtocol
	}
	return result, nil
}

type resultJSONParser struct {
	data  []byte
	index int
}

func (parser *resultJSONParser) parse() (Result, bool, bool, bool) {
	var result Result
	parser.skipWhitespace()
	if !parser.consumeByte('{') {
		return Result{}, false, false, false
	}
	parser.skipWhitespace()
	if parser.consumeByte('}') {
		parser.skipWhitespace()
		return result, false, false, parser.index == len(parser.data)
	}

	var hasSuccess, hasErrorCode bool
	for {
		key, ok := parser.canonicalString()
		if !ok {
			return Result{}, false, false, false
		}
		parser.skipWhitespace()
		if !parser.consumeByte(':') {
			return Result{}, false, false, false
		}
		parser.skipWhitespace()
		switch {
		case bytes.Equal(key, []byte("success")):
			if hasSuccess {
				return Result{}, false, false, false
			}
			if parser.consumeLiteral("true") {
				result.Success = true
			} else if parser.consumeLiteral("false") {
				result.Success = false
			} else {
				return Result{}, false, false, false
			}
			hasSuccess = true
		case bytes.Equal(key, []byte("error_code")):
			if hasErrorCode {
				return Result{}, false, false, false
			}
			value, ok := parser.canonicalString()
			if !ok {
				return Result{}, false, false, false
			}
			result.ErrorCode, ok = canonicalErrorCode(value)
			if !ok {
				return Result{}, false, false, false
			}
			hasErrorCode = true
		default:
			return Result{}, false, false, false
		}

		parser.skipWhitespace()
		if parser.consumeByte('}') {
			parser.skipWhitespace()
			return result, hasSuccess, hasErrorCode, parser.index == len(parser.data)
		}
		if !parser.consumeByte(',') {
			return Result{}, false, false, false
		}
		parser.skipWhitespace()
	}
}

func (parser *resultJSONParser) canonicalString() ([]byte, bool) {
	if !parser.consumeByte('"') {
		return nil, false
	}
	start := parser.index
	for parser.index < len(parser.data) {
		character := parser.data[parser.index]
		if character == '"' {
			value := parser.data[start:parser.index]
			parser.index++
			return value, true
		}
		if character == '\\' || character < 0x20 || character >= utf8.RuneSelf {
			return nil, false
		}
		parser.index++
	}
	return nil, false
}

func (parser *resultJSONParser) skipWhitespace() {
	for parser.index < len(parser.data) {
		switch parser.data[parser.index] {
		case ' ', '\t', '\r', '\n':
			parser.index++
		default:
			return
		}
	}
}

func (parser *resultJSONParser) consumeByte(expected byte) bool {
	if parser.index >= len(parser.data) || parser.data[parser.index] != expected {
		return false
	}
	parser.index++
	return true
}

func (parser *resultJSONParser) consumeLiteral(expected string) bool {
	if len(parser.data)-parser.index < len(expected) || !bytes.Equal(parser.data[parser.index:parser.index+len(expected)], []byte(expected)) {
		return false
	}
	parser.index += len(expected)
	return true
}

func canonicalErrorCode(value []byte) (string, bool) {
	switch {
	case bytes.Equal(value, []byte(ErrorCodePreflightFailed)):
		return ErrorCodePreflightFailed, true
	case bytes.Equal(value, []byte(ErrorCodeDatabaseConfigurationFailed)):
		return ErrorCodeDatabaseConfigurationFailed, true
	case bytes.Equal(value, []byte(ErrorCodeDatabaseMigrationFailed)):
		return ErrorCodeDatabaseMigrationFailed, true
	case bytes.Equal(value, []byte(ErrorCodeAdministratorBootstrapFailed)):
		return ErrorCodeAdministratorBootstrapFailed, true
	case bytes.Equal(value, []byte(ErrorCodeTLSProvisioningFailed)):
		return ErrorCodeTLSProvisioningFailed, true
	case bytes.Equal(value, []byte(ErrorCodeNginxConfigurationFailed)):
		return ErrorCodeNginxConfigurationFailed, true
	case bytes.Equal(value, []byte(ErrorCodeServiceActivationFailed)):
		return ErrorCodeServiceActivationFailed, true
	case bytes.Equal(value, []byte(ErrorCodeReadinessCheckFailed)):
		return ErrorCodeReadinessCheckFailed, true
	case bytes.Equal(value, []byte(ErrorCodeRollbackFailed)):
		return ErrorCodeRollbackFailed, true
	case bytes.Equal(value, []byte(ErrorCodeInternal)):
		return ErrorCodeInternal, true
	default:
		return "", false
	}
}

func validateResult(result Result) error {
	if result.Success {
		if result.ErrorCode != "" {
			return ErrInvalidResult
		}
		return nil
	}
	if _, allowed := allowedErrorCodes[result.ErrorCode]; !allowed {
		return ErrInvalidResult
	}
	return nil
}
