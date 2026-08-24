package setup

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/netip"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxSecretBytes     = 1024
	maxCIDRsPerList    = 128
	localPostgresMode  = "local"
	minimumAdminSecret = 12
)

var postgresIdentifier = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

type Secret []byte

func (secret *Secret) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return errors.New("secret must be a string")
	}
	if !utf8.ValidString(value) || len(value) > maxSecretBytes {
		return errors.New("secret is invalid")
	}
	*secret = append((*secret)[:0], []byte(value)...)
	return nil
}

func (Secret) MarshalJSON() ([]byte, error) {
	return []byte(`"[REDACTED]"`), nil
}

func (Secret) String() string { return "[REDACTED]" }

type DatabaseInput struct {
	Mode                 string `json:"mode"`
	Name                 string `json:"name"`
	Username             string `json:"username"`
	Password             Secret `json:"password"`
	PasswordConfirmation Secret `json:"password_confirmation"`
}

type DomainInput struct {
	Panel string `json:"panel"`
	Admin string `json:"admin"`
	Agent string `json:"agent"`
}

type TLSInput struct {
	Mode  string `json:"mode"`
	Email string `json:"email"`
}

type AdministratorInput struct {
	Username             string `json:"username"`
	Password             Secret `json:"password"`
	PasswordConfirmation Secret `json:"password_confirmation"`
}

type CompleteRequest struct {
	Database      DatabaseInput      `json:"database"`
	Domains       DomainInput        `json:"domains"`
	TLS           TLSInput           `json:"tls"`
	Allowlist     []string           `json:"allowlist"`
	Administrator AdministratorInput `json:"administrator"`
}

type SessionRequest struct {
	SetupCode string `json:"setup_code"`
}

var completeRequestSchema = objectSchema{
	"database": {
		"mode": nil, "name": nil, "username": nil, "password": nil, "password_confirmation": nil,
	},
	"domains": {
		"panel": nil, "admin": nil, "agent": nil,
	},
	"tls": {
		"mode": nil, "email": nil,
	},
	"allowlist": nil,
	"administrator": {
		"username": nil, "password": nil, "password_confirmation": nil,
	},
}

func DecodeSessionRequest(data []byte) (SessionRequest, error) {
	var request SessionRequest
	if err := decodeStrictObject(data, &request, objectSchema{"setup_code": nil}); err != nil {
		return SessionRequest{}, err
	}
	if len(request.SetupCode) != 64 || strings.Trim(request.SetupCode, "0123456789abcdef") != "" {
		return SessionRequest{}, errors.New("setup code is invalid")
	}
	return request, nil
}

func DecodeCompleteRequest(data []byte) (CompleteRequest, error) {
	var request CompleteRequest
	if err := decodeStrictObject(data, &request, completeRequestSchema); err != nil {
		return CompleteRequest{}, err
	}
	if err := request.Validate(); err != nil {
		request.ClearSecrets()
		return CompleteRequest{}, err
	}
	request.Allowlist, _ = normalizeCIDRs(request.Allowlist, "allowlist")
	return request, nil
}

func (request CompleteRequest) Validate() error {
	if request.Database.Mode != localPostgresMode && request.Database.Mode != "local_postgresql" {
		return errors.New("database.mode must be local")
	}
	if !postgresIdentifier.MatchString(request.Database.Name) {
		return errors.New("database.name must be a canonical PostgreSQL identifier")
	}
	if !postgresIdentifier.MatchString(request.Database.Username) {
		return errors.New("database.username must be a canonical PostgreSQL identifier")
	}
	if request.Database.Name == "postgres" || request.Database.Name == "template0" || request.Database.Name == "template1" || request.Database.Username == "postgres" {
		return errors.New("database.name or database.username uses a reserved PostgreSQL identifier")
	}
	if err := validateDatabaseSecret(request.Database.Password); err != nil {
		return err
	}
	if !secretsEqual(request.Database.Password, request.Database.PasswordConfirmation) {
		return errors.New("database password confirmation does not match")
	}
	panelHost, err := validateDomain(request.Domains.Panel, "domains.panel")
	if err != nil {
		return err
	}
	adminHost, err := validateDomain(request.Domains.Admin, "domains.admin")
	if err != nil {
		return err
	}
	agentHost, err := validateDomain(request.Domains.Agent, "domains.agent")
	if err != nil {
		return err
	}
	if domainsOverlap(panelHost, adminHost) || domainsOverlap(panelHost, agentHost) || domainsOverlap(adminHost, agentHost) {
		return errors.New("panel, admin, and agent domains must be distinct and must not contain one another")
	}
	if request.TLS.Mode != "acme" {
		return errors.New("tls.mode must be acme")
	}
	if err := validateEmail(request.TLS.Email); err != nil {
		return err
	}
	if _, err := normalizeCIDRs(request.Allowlist, "allowlist"); err != nil {
		return err
	}
	if err := validateUsername(request.Administrator.Username); err != nil {
		return err
	}
	if err := validateSecret(request.Administrator.Password, minimumAdminSecret, "administrator.password"); err != nil {
		return err
	}
	if !secretsEqual(request.Administrator.Password, request.Administrator.PasswordConfirmation) {
		return errors.New("administrator password confirmation does not match")
	}
	return nil
}

func (request CompleteRequest) Clone() CompleteRequest {
	clone := request
	clone.Database.Password = append(Secret(nil), request.Database.Password...)
	clone.Database.PasswordConfirmation = append(Secret(nil), request.Database.PasswordConfirmation...)
	clone.Administrator.Password = append(Secret(nil), request.Administrator.Password...)
	clone.Administrator.PasswordConfirmation = append(Secret(nil), request.Administrator.PasswordConfirmation...)
	clone.Allowlist = append([]string(nil), request.Allowlist...)
	return clone
}

func (request *CompleteRequest) ClearSecrets() {
	if request == nil {
		return
	}
	clear(request.Database.Password)
	request.Database.Password = nil
	clear(request.Database.PasswordConfirmation)
	request.Database.PasswordConfirmation = nil
	clear(request.Administrator.Password)
	request.Administrator.Password = nil
	clear(request.Administrator.PasswordConfirmation)
	request.Administrator.PasswordConfirmation = nil
}

func validateSecret(secret Secret, minimum int, field string) error {
	if len(secret) < minimum || len(secret) > maxSecretBytes || !utf8.Valid(secret) || bytes.IndexByte(secret, 0) >= 0 {
		return fmt.Errorf("%s must contain %d to %d valid UTF-8 bytes", field, minimum, maxSecretBytes)
	}
	return nil
}

func validateDatabaseSecret(secret Secret) error {
	if err := validateSecret(secret, minimumAdminSecret, "database.password"); err != nil {
		return err
	}
	for _, character := range string(secret) {
		if unicode.IsControl(character) {
			return errors.New("database.password must not contain control characters")
		}
	}
	return nil
}

func secretsEqual(left, right Secret) bool {
	leftDigest := sha256.Sum256(left)
	rightDigest := sha256.Sum256(right)
	return subtle.ConstantTimeCompare(leftDigest[:], rightDigest[:]) == 1
}

func validateUsername(value string) error {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 128 {
		return errors.New("administrator.username is invalid")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("administrator.username is invalid")
		}
	}
	return nil
}

func validateDomain(value, field string) (string, error) {
	if _, err := netip.ParseAddr(value); err == nil {
		return "", fmt.Errorf("%s must be a lowercase bare FQDN", field)
	}
	if value == "" || value != strings.ToLower(value) || !validDNSName(value) {
		return "", fmt.Errorf("%s must be a lowercase bare FQDN", field)
	}
	if value == "example.com" || strings.HasSuffix(value, ".example.com") {
		return "", fmt.Errorf("%s must not use the reserved example.com domain", field)
	}
	return value, nil
}

func validDNSName(host string) bool {
	if len(host) > 253 || strings.HasSuffix(host, ".") || !strings.Contains(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func domainsOverlap(left, right string) bool {
	return strings.Contains(left, right) || strings.Contains(right, left)
}

func validateEmail(value string) error {
	if value == "" || len(value) > 254 || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return errors.New("tls.email is invalid")
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value || address.Name != "" || !strings.Contains(value, "@") {
		return errors.New("tls.email is invalid")
	}
	return nil
}

func normalizeCIDRs(values []string, field string) ([]string, error) {
	if len(values) == 0 || len(values) > maxCIDRsPerList {
		return nil, fmt.Errorf("%s must contain between 1 and %d IP addresses or CIDRs", field, maxCIDRsPerList)
	}
	seen := make(map[netip.Prefix]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for index, value := range values {
		if value == "" || strings.TrimSpace(value) != value || strings.Contains(value, "%") {
			return nil, fmt.Errorf("%s[%d] is invalid", field, index)
		}
		var prefix netip.Prefix
		if address, err := netip.ParseAddr(value); err == nil {
			if address.Zone() != "" || address.Is4In6() {
				return nil, fmt.Errorf("%s[%d] is invalid", field, index)
			}
			prefix = netip.PrefixFrom(address, address.BitLen())
		} else {
			parsed, err := netip.ParsePrefix(value)
			if err != nil || !parsed.IsValid() || parsed.Addr().Zone() != "" || parsed.Addr().Is4In6() || parsed.Bits() == 0 || parsed.Masked() != parsed {
				return nil, fmt.Errorf("%s[%d] must be a canonical non-default IP address or CIDR", field, index)
			}
			prefix = parsed
		}
		if _, duplicate := seen[prefix]; duplicate {
			return nil, fmt.Errorf("%s[%d] is duplicated", field, index)
		}
		seen[prefix] = struct{}{}
		normalized = append(normalized, prefix.String())
	}
	return normalized, nil
}
