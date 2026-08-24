package setup

import (
	"bytes"
	"strings"
	"testing"
)

const validCompleteJSON = `{
  "database":{"mode":"local","name":"probe","username":"probe","password":"database secret","password_confirmation":"database secret"},
  "domains":{"panel":"panel.monitor.test","admin":"admin.monitor.test","agent":"api.monitor.test"},
  "tls":{"mode":"acme","email":"admin@example.com"},
  "allowlist":["203.0.113.25/32","2001:db8:1234::/48"],
  "administrator":{"username":"admin","password":"administrator secret","password_confirmation":"administrator secret"}
}`

func TestDecodeCompleteRequestAcceptsCanonicalLocalConfiguration(t *testing.T) {
	request, err := DecodeCompleteRequest([]byte(validCompleteJSON))
	if err != nil {
		t.Fatal(err)
	}
	if request.Database.Mode != "local" || request.Domains.Admin != "admin.monitor.test" || request.TLS.Mode != "acme" || len(request.Allowlist) != 2 {
		t.Fatalf("unexpected request: %#v", request)
	}
	if string(request.Database.Password) != "database secret" || string(request.Administrator.Password) != "administrator secret" {
		t.Fatal("secrets were not decoded")
	}
	clone := request.Clone()
	request.ClearSecrets()
	if !bytes.Equal(clone.Database.Password, []byte("database secret")) || !bytes.Equal(clone.Administrator.Password, []byte("administrator secret")) {
		t.Fatal("Clone did not deep-copy secrets")
	}
	clone.ClearSecrets()
}

func TestDecodeCompleteRequestRejectsUnknownDuplicateAndLegacyShape(t *testing.T) {
	tests := map[string]string{
		"top-level unknown":      strings.Replace(validCompleteJSON, `"database":`, `"extra":true,"database":`, 1),
		"top-level duplicate":    strings.Replace(validCompleteJSON, `"database":`, `"database":{"mode":"local"},"database":`, 1),
		"nested unknown":         strings.Replace(validCompleteJSON, `"mode":"local"`, `"host":"remote","mode":"local"`, 1),
		"nested duplicate":       strings.Replace(validCompleteJSON, `"mode":"local"`, `"mode":"local","mode":"local"`, 1),
		"legacy origins":         strings.Replace(validCompleteJSON, `"panel.monitor.test"`, `"https://panel.monitor.test"`, 1),
		"legacy split allowlist": strings.Replace(validCompleteJSON, `"allowlist":`, `"panel_allowed_cidrs":`, 1),
		"external database":      strings.Replace(validCompleteJSON, `"mode":"local"`, `"mode":"external"`, 1),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			request, err := DecodeCompleteRequest([]byte(input))
			request.ClearSecrets()
			if err == nil {
				t.Fatal("request unexpectedly accepted")
			}
		})
	}
}

func TestCompleteRequestValidationRejectsUnsafeValues(t *testing.T) {
	tests := map[string]string{
		"default IPv4 route":             strings.Replace(validCompleteJSON, `203.0.113.25/32`, `0.0.0.0/0`, 1),
		"default IPv6 route":             strings.Replace(validCompleteJSON, `2001:db8:1234::/48`, `::/0`, 1),
		"noncanonical CIDR":              strings.Replace(validCompleteJSON, `203.0.113.25/32`, `203.0.113.25/24`, 1),
		"duplicate CIDR":                 strings.Replace(validCompleteJSON, `"203.0.113.25/32","2001:db8:1234::/48"`, `"203.0.113.25/32","203.0.113.25/32"`, 1),
		"short admin password":           strings.Replace(validCompleteJSON, `administrator secret`, `short`, 1),
		"database confirmation mismatch": strings.Replace(validCompleteJSON, `"password_confirmation":"database secret"`, `"password_confirmation":"different database secret"`, 1),
		"admin confirmation mismatch":    strings.Replace(validCompleteJSON, `"password_confirmation":"administrator secret"`, `"password_confirmation":"different administrator secret"`, 1),
		"IP domain":                      strings.Replace(validCompleteJSON, `panel.monitor.test`, `192.0.2.1`, 1),
		"uppercase domain":               strings.Replace(validCompleteJSON, `panel.monitor.test`, `Panel.monitor.test`, 1),
		"duplicate domain":               strings.Replace(validCompleteJSON, `admin.monitor.test`, `panel.monitor.test`, 1),
		"overlapping domain":             strings.Replace(validCompleteJSON, `admin.monitor.test`, `monitor.test`, 1),
		"reserved example domain":        strings.Replace(validCompleteJSON, `panel.monitor.test`, `panel.example.com`, 1),
		"non-ACME TLS":                   strings.Replace(validCompleteJSON, `"mode":"acme"`, `"mode":"manual"`, 1),
		"display-name email":             strings.Replace(validCompleteJSON, `admin@example.com`, `Admin <admin@example.com>`, 1),
		"database newline":               strings.Replace(validCompleteJSON, `database secret`, `database\nsecret`, 2),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			request, err := DecodeCompleteRequest([]byte(input))
			request.ClearSecrets()
			if err == nil {
				t.Fatal("unsafe value unexpectedly accepted")
			}
		})
	}
}

func TestAllowlistAcceptsBareIPsAndNormalizesThem(t *testing.T) {
	input := strings.Replace(validCompleteJSON, `"203.0.113.25/32","2001:db8:1234::/48"`, `"203.0.113.25","2001:db8::1"`, 1)
	request, err := DecodeCompleteRequest([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	defer request.ClearSecrets()
	if strings.Join(request.Allowlist, ",") != "203.0.113.25/32,2001:db8::1/128" {
		t.Fatalf("normalized allowlist = %#v", request.Allowlist)
	}
}

func TestSessionRequestIsStrictAndRequiresCanonical256BitCode(t *testing.T) {
	valid := strings.Repeat("a", 64)
	request, err := DecodeSessionRequest([]byte(`{"setup_code":"` + valid + `"}`))
	if err != nil || request.SetupCode != valid {
		t.Fatalf("valid code rejected: %v", err)
	}
	for _, input := range []string{
		`{"setup_code":"short"}`,
		`{"setup_code":"` + strings.ToUpper(valid) + `"}`,
		`{"setup_code":"` + valid + `","extra":true}`,
		`{"setup_code":"` + valid + `","setup_code":"` + valid + `"}`,
		`[]`,
	} {
		if _, err := DecodeSessionRequest([]byte(input)); err == nil {
			t.Fatalf("invalid input accepted: %s", input)
		}
	}
}
