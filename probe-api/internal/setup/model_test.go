package setup

import (
	"bytes"
	"strings"
	"testing"
)

const validCompleteJSON = `{
	"database":{"mode":"local","name":"probe","username":"probe","password":"database secret","password_confirmation":"database secret"},
	"domains":{"panel":"panel.monitor.test","admin":"admin.monitor.test","agent":"api.monitor.test"},
	"network":{"address":""},
	"tls":{"mode":"acme","email":"admin@example.com"},
  "allowlist":["203.0.113.25/32","2001:db8:1234::/48"],
  "administrator":{"username":"admin","password":"administrator secret","password_confirmation":"administrator secret"}
}`

func TestDecodeCompleteRequestAcceptsCanonicalLocalConfiguration(t *testing.T) {
	request, err := DecodeCompleteRequest([]byte(validCompleteJSON))
	if err != nil {
		t.Fatal(err)
	}
	if request.Database.Mode != "local" || request.Domains.Admin != "admin.monitor.test" || request.Network.Address != "" || request.TLS.Mode != "acme" || len(request.Allowlist) != 2 {
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

func TestCompleteRequestAcceptsPrivateCAIPIngressAndBuildsCanonicalOrigins(t *testing.T) {
	for _, address := range []string{"10.20.30.40", "fd00::25", "2001:db8::25"} {
		input := privateCACompleteJSON(address)
		request, err := DecodeCompleteRequest([]byte(input))
		if err != nil {
			t.Fatalf("address %s rejected: %v", address, err)
		}
		access, err := request.AccessConfiguration()
		if err != nil || access.Mode != IngressModeIP || access.Address.String() != address {
			t.Fatalf("address %s access = %#v, error = %v", address, access, err)
		}
		if address == "10.20.30.40" && (access.PanelOrigin != "https://10.20.30.40:18453" || access.AgentOrigin != "https://10.20.30.40:18454" || access.AdminOrigin != "https://10.20.30.40:18455") {
			t.Fatalf("IPv4 origins = %#v", access)
		}
		if address == "2001:db8::25" && access.AdminOrigin != "https://[2001:db8::25]:18455" {
			t.Fatalf("IPv6 admin origin = %q", access.AdminOrigin)
		}
		request.ClearSecrets()
	}
}

func TestManagementProfileBuildsOnlyAdministratorOrigins(t *testing.T) {
	for name, input := range map[string]string{
		"domain": managementCompleteJSON("admin.monitor.test", ""),
		"IPv4":   managementCompleteJSON("", "10.20.30.40"),
		"IPv6":   managementCompleteJSON("", "2001:db8::25"),
	} {
		t.Run(name, func(t *testing.T) {
			request, err := DecodeCompleteRequestForProfile([]byte(input), InstallationProfileManagement)
			if err != nil {
				t.Fatal(err)
			}
			defer request.ClearSecrets()
			access, err := request.AccessConfiguration()
			if err != nil {
				t.Fatal(err)
			}
			if access.Profile != InstallationProfileManagement || access.PanelOrigin != "" || access.AgentOrigin != "" {
				t.Fatalf("management access = %#v", access)
			}
			if name == "domain" && (access.Mode != IngressModeDomain || access.AdminOrigin != "https://admin.monitor.test") {
				t.Fatalf("domain management access = %#v", access)
			}
			if name == "IPv4" && access.AdminOrigin != "https://10.20.30.40:18455" {
				t.Fatalf("IPv4 management origin = %q", access.AdminOrigin)
			}
			if name == "IPv6" && access.AdminOrigin != "https://[2001:db8::25]:18455" {
				t.Fatalf("IPv6 management origin = %q", access.AdminOrigin)
			}
		})
	}
}

func TestFixedSetupProfileCannotBeChangedByBrowser(t *testing.T) {
	management := managementCompleteJSON("admin.monitor.test", "")
	conflicting := strings.Replace(management, `"database":`, `"profile":"full","database":`, 1)
	request, err := DecodeCompleteRequestForProfile([]byte(conflicting), InstallationProfileManagement)
	request.ClearSecrets()
	if err == nil || !strings.Contains(err.Error(), "fixed setup profile") {
		t.Fatalf("conflicting browser profile error = %v", err)
	}

	unsafeDomains := strings.Replace(management, `"domains":{"admin":`, `"domains":{"panel":"panel.monitor.test","admin":`, 1)
	request, err = DecodeCompleteRequestForProfile([]byte(unsafeDomains), InstallationProfileManagement)
	request.ClearSecrets()
	if err == nil || !strings.Contains(err.Error(), `unknown field "panel"`) {
		t.Fatalf("management visitor domain error = %v", err)
	}

	missingAdmin := strings.Replace(management, `"domains":{"admin":"admin.monitor.test"}`, `"domains":{}`, 1)
	request, err = DecodeCompleteRequestForProfile([]byte(missingAdmin), InstallationProfileManagement)
	request.ClearSecrets()
	if err == nil || !strings.Contains(err.Error(), "must contain only admin") {
		t.Fatalf("management missing admin error = %v", err)
	}
}

func TestCompleteRequestRejectsAmbiguousOrUnsafeIngress(t *testing.T) {
	ipBase := privateCACompleteJSON("10.20.30.40")
	tests := map[string]string{
		"partial domains":                strings.Replace(validCompleteJSON, `"admin":"admin.monitor.test"`, `"admin":""`, 1),
		"domain mode address":            strings.Replace(validCompleteJSON, `"address":""`, `"address":"10.20.30.40"`, 1),
		"domain mode missing network":    strings.Replace(validCompleteJSON, `"network":{"address":""},`, ``, 1),
		"IP mode missing domains object": strings.Replace(ipBase, `"domains":{"panel":"","admin":"","agent":""},`, ``, 1),
		"IP mode missing domain member":  strings.Replace(ipBase, `"domains":{"panel":"","admin":"","agent":""}`, `"domains":{"panel":"","admin":""}`, 1),
		"IP mode missing address":        strings.Replace(ipBase, `"address":"10.20.30.40"`, `"address":""`, 1),
		"IP mode missing address member": strings.Replace(ipBase, `"network":{"address":"10.20.30.40"}`, `"network":{}`, 1),
		"IP mode ACME":                   strings.Replace(ipBase, `"mode":"private_ca"`, `"mode":"acme"`, 1),
		"IP mode email":                  strings.Replace(ipBase, `"email":""`, `"email":"admin@example.com"`, 1),
		"IP mode missing email member":   strings.Replace(ipBase, `,"email":""`, ``, 1),
		"loopback IPv4":                  strings.Replace(ipBase, `10.20.30.40`, `127.0.0.1`, 1),
		"unspecified IPv4":               strings.Replace(ipBase, `10.20.30.40`, `0.0.0.0`, 1),
		"link-local IPv4":                strings.Replace(ipBase, `10.20.30.40`, `169.254.1.1`, 1),
		"multicast IPv4":                 strings.Replace(ipBase, `10.20.30.40`, `224.0.0.1`, 1),
		"loopback IPv6":                  strings.Replace(ipBase, `10.20.30.40`, `::1`, 1),
		"unspecified IPv6":               strings.Replace(ipBase, `10.20.30.40`, `::`, 1),
		"link-local IPv6":                strings.Replace(ipBase, `10.20.30.40`, `fe80::1`, 1),
		"scoped IPv6":                    strings.Replace(ipBase, `10.20.30.40`, `fe80::1%eth0`, 1),
		"multicast IPv6":                 strings.Replace(ipBase, `10.20.30.40`, `ff02::1`, 1),
		"mapped IPv6":                    strings.Replace(ipBase, `10.20.30.40`, `::ffff:192.0.2.1`, 1),
		"noncanonical IPv6":              strings.Replace(ipBase, `10.20.30.40`, `2001:0db8::1`, 1),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			request, err := DecodeCompleteRequest([]byte(input))
			request.ClearSecrets()
			if err == nil {
				t.Fatal("unsafe or ambiguous ingress unexpectedly accepted")
			}
		})
	}
}

func privateCACompleteJSON(address string) string {
	input := strings.Replace(validCompleteJSON,
		`"domains":{"panel":"panel.monitor.test","admin":"admin.monitor.test","agent":"api.monitor.test"}`,
		`"domains":{"panel":"","admin":"","agent":""}`, 1)
	input = strings.Replace(input, `"network":{"address":""}`, `"network":{"address":"`+address+`"}`, 1)
	return strings.Replace(input, `"tls":{"mode":"acme","email":"admin@example.com"}`, `"tls":{"mode":"private_ca","email":""}`, 1)
}

func managementCompleteJSON(adminDomain, address string) string {
	input := strings.Replace(validCompleteJSON,
		`"domains":{"panel":"panel.monitor.test","admin":"admin.monitor.test","agent":"api.monitor.test"}`,
		`"domains":{"admin":"`+adminDomain+`"}`, 1)
	if address != "" {
		input = strings.Replace(input, `"network":{"address":""}`, `"network":{"address":"`+address+`"}`, 1)
		input = strings.Replace(input, `"tls":{"mode":"acme","email":"admin@example.com"}`, `"tls":{"mode":"private_ca","email":""}`, 1)
	}
	return input
}

func TestDecodeCompleteRequestRejectsUnknownDuplicateAndLegacyShape(t *testing.T) {
	tests := map[string]string{
		"top-level unknown":      strings.Replace(validCompleteJSON, `"database":`, `"extra":true,"database":`, 1),
		"top-level duplicate":    strings.Replace(validCompleteJSON, `"database":`, `"database":{"mode":"local"},"database":`, 1),
		"nested unknown":         strings.Replace(validCompleteJSON, `"mode":"local"`, `"host":"remote","mode":"local"`, 1),
		"nested duplicate":       strings.Replace(validCompleteJSON, `"mode":"local"`, `"mode":"local","mode":"local"`, 1),
		"legacy origins":         strings.Replace(validCompleteJSON, `"panel.monitor.test"`, `"https://panel.monitor.test"`, 1),
		"legacy split allowlist": strings.Replace(validCompleteJSON, `"allowlist":`, `"panel_allowed_cidrs":`, 1),
		"legacy database mode":   strings.Replace(validCompleteJSON, `"mode":"local"`, `"mode":"local_postgresql"`, 1),
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
