package config

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("PROBE_API_LISTEN_ADDR", "")
	t.Setenv("PROBE_DATABASE_URL", "")
	t.Setenv("PROBE_LOG_LEVEL", "")
	t.Setenv("PROBE_AGENT_INSTALLER_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ListenAddress != "127.0.0.1:8080" {
		t.Fatalf("ListenAddress = %q", cfg.ListenAddress)
	}
	if cfg.ReadTimeout != 15*time.Second {
		t.Fatalf("ReadTimeout = %s", cfg.ReadTimeout)
	}
	if cfg.AgentPublicURL != "https://api.example.com" || cfg.AgentInstallerURL != defaultAgentInstallerURL || len(cfg.AgentInstallCAPEM) != 0 {
		t.Fatalf("unexpected Agent bootstrap defaults: url=%q installer=%q ca_bytes=%d", cfg.AgentPublicURL, cfg.AgentInstallerURL, len(cfg.AgentInstallCAPEM))
	}
	if err := cfg.ValidateDatabase(); err == nil {
		t.Fatal("ValidateDatabase() accepted an empty URL")
	}
	if err := cfg.ValidateServe(); err == nil {
		t.Fatal("ValidateServe() accepted the example Agent origin")
	}
}

func TestValidateServeRequiresAnExplicitAgentOrigin(t *testing.T) {
	cfg := Config{AgentPublicURL: "https://agent.example.net:8443", AgentInstallerURL: defaultAgentInstallerURL}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe() rejected an explicit Agent origin: %v", err)
	}
}

func TestLoadValidatesAgentBootstrapOriginAndPublicCA(t *testing.T) {
	t.Setenv("PROBE_AGENT_PUBLIC_URL", "http://agent.example.com")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a non-HTTPS Agent origin")
	}

	t.Setenv("PROBE_AGENT_PUBLIC_URL", "https://agent.example.com/path")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an Agent origin with a path")
	}

	t.Setenv("PROBE_AGENT_PUBLIC_URL", "https://agent.example.com:8443")
	t.Setenv("PROBE_AGENT_INSTALL_CA_FILE", "relative-ca.pem")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a relative CA path")
	}

	caPath := filepath.Join(t.TempDir(), "agent-ca.pem")
	if err := os.WriteFile(caPath, testCACertificatePEM(t), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROBE_AGENT_INSTALL_CA_FILE", caPath)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() rejected a valid public CA: %v", err)
	}
	if cfg.AgentPublicURL != "https://agent.example.com:8443" || len(cfg.AgentInstallCAPEM) == 0 {
		t.Fatalf("unexpected Agent bootstrap configuration: url=%q ca_bytes=%d", cfg.AgentPublicURL, len(cfg.AgentInstallCAPEM))
	}

	if err := os.WriteFile(caPath, []byte("-----BEGIN PRIVATE KEY-----\nAA==\n-----END PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted private key material as the public CA bundle")
	}

	if err := os.WriteFile(caPath, bytes.Repeat([]byte("x"), 64*1024+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a CA bundle larger than the configured public CA size limit")
	}
}

func TestLoadValidatesAgentInstallerURL(t *testing.T) {
	for _, value := range []string{
		"http://raw.example/install.sh",
		"https://raw.example",
		"https://raw.example/deploy/../install.sh",
		"https://raw.example/install.sh?branch=main",
		"https://user@raw.example/install.sh",
		"https://raw.githubusercontent.com/Kcmose/my-agent/main/deploy/install.sh",
		"https://raw.githubusercontent.com/Kcmose/my-agent/refs/heads/main/deploy/install.sh",
		"https://raw.githubusercontent.com/Kcmose/my-agent/0123456789abcdef/deploy/install.sh",
		"https://raw.githubusercontent.com/Kcmose/my-agent/v1/deploy/install.sh",
		"https://raw.githubusercontent.com/Kcmose/my-agent/v1.0/deploy/install.sh",
		"https://raw.githubusercontent.com/Kcmose/my-agent/v1.0.0/deploy/install.sh",
		"https://raw.githubusercontent.com/Kcmose/my-agent/v01.0.0/deploy/install.sh",
		"https://raw.githubusercontent.com/Kcmose/my-agent/v1.0.0-beta/deploy/install.sh",
		"https://raw.githubusercontent.com/Kcmose/my-agent/V1.0.0/deploy/install.sh",
		"https://raw.githubusercontent.com/Kcmose/my-agent/refs/tags/v1/deploy/install.sh",
		"https://raw.githubusercontent.com/Kcmose/my-agent/refs/tags/v01.0.0/deploy/install.sh",
		"https://raw.githubusercontent.com/Kcmose/my-agent/refs/heads/v1.0.0/deploy/install.sh",
		"https://raw.githubusercontent.com/other/my-agent/0123456789012345678901234567890123456789/deploy/install.sh",
		"https://raw.githubusercontent.com/Kcmose/my-agent/0123456789012345678901234567890123456789/install.sh",
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("PROBE_AGENT_INSTALLER_URL", value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted unsafe Agent installer URL %q", value)
			}
		})
	}
	for _, value := range []string{
		defaultAgentInstallerURL,
		"https://raw.githubusercontent.com/Kcmose/my-agent/0123456789012345678901234567890123456789/deploy/install.sh",
	} {
		t.Run("valid-"+value, func(t *testing.T) {
			t.Setenv("PROBE_AGENT_INSTALLER_URL", value)
			if _, err := Load(); err != nil {
				t.Fatalf("Load() rejected immutable GitHub installer URL %q: %v", value, err)
			}
		})
	}
}

func testCACertificatePEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Agent installation test CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestLoadRejectsInvalidPoolBounds(t *testing.T) {
	t.Setenv("PROBE_DATABASE_MAX_CONNS", "2")
	t.Setenv("PROBE_DATABASE_MIN_CONNS", "3")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted invalid pool bounds")
	}
}

func TestLoadStageThreeDefaults(t *testing.T) {
	t.Setenv("PROBE_ADMIN_ORIGIN", "")
	t.Setenv("PROBE_SESSION_TTL", "")
	t.Setenv("PROBE_SESSION_MAX_PER_USER", "")
	t.Setenv("PROBE_SESSION_REVOKED_RETENTION", "")
	t.Setenv("PROBE_NODE_OFFLINE_AFTER", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AdminOrigin != "https://admin.example.com" || cfg.SessionTTL != 12*time.Hour ||
		cfg.SessionMaxPerUser != 5 || cfg.SessionRetention != 24*time.Hour || cfg.NodeOfflineAfter != 45*time.Second {
		t.Fatalf("unexpected Stage 3 defaults: %+v", cfg)
	}
}

func TestLoadRejectsUnsafeAdminOriginAndSessionBounds(t *testing.T) {
	t.Setenv("PROBE_ADMIN_ORIGIN", "http://admin.example.com")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a non-HTTPS management origin")
	}

	t.Setenv("PROBE_ADMIN_ORIGIN", "https://admin.example.com")
	t.Setenv("PROBE_SESSION_MAX_PER_USER", "0")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted zero concurrent sessions")
	}

	t.Setenv("PROBE_SESSION_MAX_PER_USER", "5")
	t.Setenv("PROBE_ADMIN_ORIGIN", "https://admin.example.com#")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a non-canonical management origin")
	}
}

func TestLoadRejectsNonLoopbackListenAddress(t *testing.T) {
	for _, value := range []string{"0.0.0.0:8080", "192.0.2.10:8080", "localhost:8080", "127.0.0.1:0"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("PROBE_API_LISTEN_ADDR", value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted unsafe listen address %q", value)
			}
		})
	}
	t.Setenv("PROBE_API_LISTEN_ADDR", "[::1]:8080")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() rejected IPv6 loopback: %v", err)
	}
}

func TestLoadReadsSharedAllowlistAndTrustedProxies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-allowlist.geo")
	if err := os.WriteFile(path, []byte("192.0.2.0/24 1;\n2001:db8::10 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROBE_ADMIN_ALLOWLIST_FILE", path)
	t.Setenv("PROBE_TRUSTED_PROXY_CIDRS", "127.0.0.1,::1")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AdminAllowlist.Len() != 2 || cfg.TrustedProxies.Len() != 2 {
		t.Fatalf("unexpected CIDR configuration: allowlist=%d proxies=%d", cfg.AdminAllowlist.Len(), cfg.TrustedProxies.Len())
	}
}

func TestLoadRejectsUnsafeCIDRsAndRateBounds(t *testing.T) {
	t.Setenv("PROBE_TRUSTED_PROXY_CIDRS", "0.0.0.0/0")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a default trusted proxy route")
	}
	t.Setenv("PROBE_TRUSTED_PROXY_CIDRS", "127.0.0.1/32")
	t.Setenv("PROBE_LOGIN_USERNAME_LIMIT", "0")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an invalid login limit")
	}
}
