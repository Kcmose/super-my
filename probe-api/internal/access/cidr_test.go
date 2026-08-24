package access

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCIDRListNormalizesAndMatches(t *testing.T) {
	set, err := ParseCIDRList("192.0.2.10, 198.51.100.0/24, 2001:db8::/48, ::ffff:203.0.113.8")
	if err != nil {
		t.Fatalf("ParseCIDRList() error = %v", err)
	}
	for _, value := range []string{"192.0.2.10", "198.51.100.99", "2001:db8::8", "203.0.113.8"} {
		if !set.Contains(netip.MustParseAddr(value)) {
			t.Errorf("set does not contain %s", value)
		}
	}
	if set.Contains(netip.MustParseAddr("203.0.113.9")) {
		t.Fatal("set matched an address outside its prefixes")
	}
}

func TestParseCIDRListRejectsUnsafeOrAmbiguousValues(t *testing.T) {
	for _, value := range []string{
		"0.0.0.0/0", "::/0", "example.com", "192.0.2.1:443",
		"fe80::1%eth0", "192.0.2.1,192.0.2.1/32", "192.0.2.1,",
	} {
		if _, err := ParseCIDRList(value); err == nil {
			t.Errorf("ParseCIDRList(%q) unexpectedly succeeded", value)
		}
	}
}

func TestLoadNginxGeoAllowlist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-allowlist.geo")
	content := "# management clients\n192.0.2.10 1;\n2001:db8:1234::/48 1; # office\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := LoadNginxGeoAllowlist(path)
	if err != nil {
		t.Fatalf("LoadNginxGeoAllowlist() error = %v", err)
	}
	if set.Len() != 2 || !set.Contains(netip.MustParseAddr("2001:db8:1234::9")) {
		t.Fatalf("unexpected parsed allowlist: len=%d", set.Len())
	}
}

func TestLoadNginxGeoAllowlistRejectsInvalidLinesAndDefaultRoutes(t *testing.T) {
	for name, content := range map[string]string{
		"default":   "0.0.0.0/0 1;\n",
		"deny":      "192.0.2.10 0;\n",
		"directive": "include other.conf;\n",
		"duplicate": "192.0.2.10 1;\n192.0.2.10/32 1;\n",
		"mapped":    "::ffff:192.0.2.10 1;\n",
		"unmasked":  "192.0.2.10/24 1;\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "allowlist.geo")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadNginxGeoAllowlist(path); err == nil || strings.TrimSpace(err.Error()) == "" {
				t.Fatal("invalid allowlist unexpectedly succeeded")
			}
		})
	}
}
