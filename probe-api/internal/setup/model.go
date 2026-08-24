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
	Panel   string `json:"panel"`
	Admin   string `json:"admin"`
	Agent   string `json:"agent"`
	decoded bool
}

type NetworkInput struct {
	Address string `json:"address"`
	decoded bool
}

type TLSInput struct {
	Mode    string `json:"mode"`
	Email   string `json:"email"`
	decoded bool
}

func (input *DomainInput) UnmarshalJSON(data []byte) error {
	var wire struct {
		Panel *string `json:"panel"`
		Admin *string `json:"admin"`
		Agent *string `json:"agent"`
	}
	if err := json.Unmarshal(data, &wire); err != nil || wire.Panel == nil || wire.Admin == nil || wire.Agent == nil {
		return errors.New("domains must contain panel, admin, and agent")
	}
	input.Panel, input.Admin, input.Agent = *wire.Panel, *wire.Admin, *wire.Agent
	input.decoded = true
	return nil
}

func (input *NetworkInput) UnmarshalJSON(data []byte) error {
	var wire struct {
		Address *string `json:"address"`
	}
	if err := json.Unmarshal(data, &wire); err != nil || wire.Address == nil {
		return errors.New("network must contain address")
	}
	input.Address = *wire.Address
	input.decoded = true
	return nil
}

func (input *TLSInput) UnmarshalJSON(data []byte) error {
	var wire struct {
		Mode  *string `json:"mode"`
		Email *string `json:"email"`
	}
	if err := json.Unmarshal(data, &wire); err != nil || wire.Mode == nil || wire.Email == nil {
		return errors.New("tls must contain mode and email")
	}
	input.Mode, input.Email = *wire.Mode, *wire.Email
	input.decoded = true
	return nil
}

type AdministratorInput struct {
	Username             string `json:"username"`
	Password             Secret `json:"password"`
	PasswordConfirmation Secret `json:"password_confirmation"`
}

type CompleteRequest struct {
	Database      DatabaseInput      `json:"database"`
	Domains       DomainInput        `json:"domains"`
	Network       NetworkInput       `json:"network"`
	TLS           TLSInput           `json:"tls"`
	Allowlist     []string           `json:"allowlist"`
	Administrator AdministratorInput `json:"administrator"`
}

var completeRequestSchema = objectSchema{
	"database": {
		"mode": nil, "name": nil, "username": nil, "password": nil, "password_confirmation": nil,
	},
	"domains": {
		"panel": nil, "admin": nil, "agent": nil,
	},
	"network": {
		"address": nil,
	},
	"tls": {
		"mode": nil, "email": nil,
	},
	"allowlist": nil,
	"administrator": {
		"username": nil, "password": nil, "password_confirmation": nil,
	},
}

func DecodeCompleteRequest(data []byte) (CompleteRequest, error) {
	var request CompleteRequest
	if err := decodeStrictObject(data, &request, completeRequestSchema); err != nil {
		request.ClearSecrets()
		return CompleteRequest{}, err
	}
	// Empty strings are meaningful in the mutually exclusive IP ingress mode,
	// so semantic validation alone cannot distinguish an explicitly empty field
	// from an omitted object/member. Keep the wire contract structurally strict.
	if !request.Domains.decoded || !request.Network.decoded || !request.TLS.decoded {
		request.ClearSecrets()
		return CompleteRequest{}, errors.New("request is missing domains, network, or tls")
	}
	if err := request.Validate(); err != nil {
		request.ClearSecrets()
		return CompleteRequest{}, err
	}
	request.Allowlist, _ = normalizeCIDRs(request.Allowlist, "allowlist")
	return request, nil
}

func (request CompleteRequest) Validate() error {
	if request.Database.Mode != localPostgresMode {
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
	if _, err := request.AccessConfiguration(); err != nil {
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
