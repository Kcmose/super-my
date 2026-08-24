package setupfinalize

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"probe-api/internal/setup"
)

type fakeRunner struct {
	certificateRoot string
	domains         []string
	commands        []string
	sensitiveInputs [][]byte
}

func (runner *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	runner.commands = append(runner.commands, name+" "+strings.Join(args, " "))
	if filepathBase(name) == "certbot" {
		return writeTestCertificate(runner.certificateRoot, runner.domains)
	}
	return nil
}

func (runner *fakeRunner) RunQuiet(ctx context.Context, name string, args ...string) error {
	return runner.Run(ctx, name, args...)
}

func (runner *fakeRunner) RunSensitive(_ context.Context, stdin []byte, name string, args ...string) error {
	runner.commands = append(runner.commands, name+" "+strings.Join(args, " "))
	runner.sensitiveInputs = append(runner.sensitiveInputs, append([]byte(nil), stdin...))
	return nil
}

func (runner *fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	runner.commands = append(runner.commands, name+" "+strings.Join(args, " "))
	if filepathBase(name) == "ss" {
		return []byte("LISTEN 0 128 127.0.0.1:5432 0.0.0.0:*\n"), nil
	}
	if filepathBase(name) == "setpriv" {
		return []byte("no\n"), nil
	}
	return nil, nil
}

type fakeBootstrapper struct {
	databasePassword string
	adminPassword    string
	called           bool
}

func (bootstrapper *fakeBootstrapper) MigrateAndBootstrap(_ context.Context, database DatabaseConfig, _ string, administratorPassword []byte) error {
	bootstrapper.called = true
	bootstrapper.databasePassword = string(database.Password)
	bootstrapper.adminPassword = string(administratorPassword)
	clear(database.Password)
	clear(administratorPassword)
	return nil
}

func TestFinalizerCompletesFreshInstallationWithoutPuttingSecretsInCommands(t *testing.T) {
	base := t.TempDir()
	paths := testPaths(base)
	for _, directory := range []string{
		filepath.Join(base, "srv"), filepath.Join(base, "var", "backups"),
		filepath.Join(base, "etc"), filepath.Join(base, "etc", "probe-panel"),
		paths.NginxConfD, paths.NginxSitesEnabled, filepath.Dir(paths.APIUnit),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bundle := filepath.Join(base, "bundle")
	writeTestBundle(t, bundle)
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	domains := []string{"panel.monitor.test", "admin.monitor.test", "api.monitor.test"}
	runner := &fakeRunner{certificateRoot: paths.LetsEncryptLive, domains: domains}
	bootstrapper := &fakeBootstrapper{}
	currentIdentity := Identity{UID: os.Getuid(), GID: os.Getgid()}
	finalizer, err := New(Config{
		BundleRoot: bundle, ReleaseID: "v1.0.0", Paths: paths,
		Runner: runner, Bootstrapper: bootstrapper,
		IdentityLookup: func(string) (Identity, error) { return currentIdentity, nil },
		RootIdentity:   currentIdentity,
		RequireRoot:    false, Now: func() time.Time { return now },
		ResolveHostname: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := setup.CompleteRequest{
		Database: setup.DatabaseInput{
			Mode: "local", Name: "probe", Username: "probe",
			Password: setup.Secret("db-secret-1234"), PasswordConfirmation: setup.Secret("db-secret-1234"),
		},
		Domains:   setup.DomainInput{Panel: domains[0], Admin: domains[1], Agent: domains[2]},
		TLS:       setup.TLSInput{Mode: "acme", Email: "admin@monitor.test"},
		Allowlist: []string{"203.0.113.25", "2001:db8:1234::/48"},
		Administrator: setup.AdministratorInput{
			Username: "admin", Password: setup.Secret("admin-secret-1234"), PasswordConfirmation: setup.Secret("admin-secret-1234"),
		},
	}
	if err := finalizer.Finalize(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !bootstrapper.called || bootstrapper.databasePassword != "db-secret-1234" || bootstrapper.adminPassword != "admin-secret-1234" {
		t.Fatalf("application bootstrap input = %#v", bootstrapper)
	}
	for _, command := range runner.commands {
		if strings.Contains(command, "db-secret-1234") || strings.Contains(command, "admin-secret-1234") {
			t.Fatalf("secret leaked into command: %s", command)
		}
	}
	if len(runner.sensitiveInputs) != 2 || !strings.Contains(string(runner.sensitiveInputs[0]), "db-secret-1234") {
		t.Fatal("PostgreSQL role password was not sent only through sensitive stdin")
	}
	apiEnvironment, err := os.ReadFile(paths.APIEnvironment)
	if err != nil || !strings.Contains(string(apiEnvironment), "PROBE_ADMIN_ORIGIN=https://admin.monitor.test") {
		t.Fatalf("API environment was not finalized: %v", err)
	}
	allowlist, err := os.ReadFile(paths.Allowlist)
	if err != nil || string(allowlist) != "203.0.113.25/32 1;\n2001:db8:1234::/48 1;\n" {
		t.Fatalf("allowlist = %q, error = %v", allowlist, err)
	}
	linkTarget, err := os.Readlink(filepath.Join(paths.TLSRoot, "admin", "fullchain.pem"))
	if err != nil || linkTarget != filepath.Join(paths.LetsEncryptLive, "fullchain.pem") {
		t.Fatalf("administrator certificate link = %q, error = %v", linkTarget, err)
	}
}

func TestRenderAndListenerContractsFailClosed(t *testing.T) {
	if _, err := renderAllowlist([]string{"0.0.0.0/0"}); err == nil {
		t.Fatal("default allowlist route accepted")
	}
	if !wildcardListener([]byte("LISTEN 0 128 0.0.0.0:5432 0.0.0.0:*\n"), "5432") {
		t.Fatal("wildcard PostgreSQL listener not detected")
	}
	if wildcardListener([]byte("LISTEN 0 128 127.0.0.1:5432 0.0.0.0:*\n"), "5432") {
		t.Fatal("loopback PostgreSQL listener rejected")
	}
	if _, err := replaceEnvironment([]byte("A=one\nA=two\n"), map[string]string{"A": "three"}); err == nil {
		t.Fatal("duplicate environment key accepted")
	}
}

func testPaths(base string) Paths {
	probeRoot := filepath.Join(base, "srv", "probe")
	configRoot := filepath.Join(probeRoot, "config")
	return Paths{
		ProbeRoot: probeRoot, APIPath: filepath.Join(probeRoot, "api", "probe-api"),
		AgentPath: filepath.Join(probeRoot, "agent"), WebPath: filepath.Join(probeRoot, "web"),
		AdminPath: filepath.Join(probeRoot, "admin"), MigrationsPath: filepath.Join(probeRoot, "migrations"),
		ConfigDir: configRoot, NginxConfigDir: filepath.Join(configRoot, "nginx"),
		BackupScriptDir: filepath.Join(probeRoot, "api", "scripts"), ReleaseDir: filepath.Join(probeRoot, "releases"),
		BackupDir: filepath.Join(probeRoot, "backups"), PostgresBackupDir: filepath.Join(base, "var", "backups", "probe-panel", "postgres"),
		APIEnvironment: filepath.Join(configRoot, "probe-api.env"), BackupEnvironment: filepath.Join(configRoot, "probe-postgres-backup.env"),
		PGPass: filepath.Join(configRoot, "probe-postgres.pgpass"), Allowlist: filepath.Join(base, "etc", "probe-panel", "admin-allowlist.geo"),
		ActiveNginxConfig: filepath.Join(configRoot, "nginx", "nginx.conf"), NginxLink: filepath.Join(base, "etc", "nginx", "conf.d", "probe-panel.conf"),
		NginxConfD: filepath.Join(base, "etc", "nginx", "conf.d"), NginxSitesEnabled: filepath.Join(base, "etc", "nginx", "sites-enabled"),
		TLSRoot: filepath.Join(base, "etc", "probe-panel", "tls"), LetsEncryptLive: filepath.Join(base, "etc", "letsencrypt", "live", "probe-panel"),
		LetsEncryptDeployHook: filepath.Join(base, "etc", "letsencrypt", "renewal-hooks", "deploy", "probe-panel"),
		APIUnit:               filepath.Join(base, "etc", "systemd", "system", "probe-api.service"),
		BackupUnit:            filepath.Join(base, "etc", "systemd", "system", "probe-postgres-backup.service"),
		BackupTimerUnit:       filepath.Join(base, "etc", "systemd", "system", "probe-postgres-backup.timer"),
	}
}

func writeTestBundle(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"BUNDLE-SHA256SUMS":                                  "placeholder\n",
		"artifacts/api/probe-api":                            "binary\n",
		"source/probe-api/deploy/scripts/install-release.sh": "#!/bin/sh\nexit 0\n",
		"source/probe-api/deploy/nginx/nginx.conf":           "panel.example.com admin.example.com api.example.com\n",
		"source/probe-api/config/probe-api.env.example": strings.Join([]string{
			"PROBE_API_LISTEN_ADDR=127.0.0.1:8080", "PROBE_DATABASE_URL=postgresql://placeholder",
			"PROBE_ADMIN_ORIGIN=https://admin.example.com", "PROBE_AGENT_PUBLIC_URL=https://api.example.com",
			"PROBE_AGENT_INSTALLER_URL=https://raw.githubusercontent.com/Kcmose/my-agent/refs/tags/v1.0.1/deploy/install.sh",
			"PROBE_ADMIN_ALLOWLIST_FILE=/etc/probe-panel/admin-allowlist.geo", "PROBE_TRUSTED_PROXY_CIDRS=127.0.0.1/32,::1/128", "",
		}, "\n"),
		"source/probe-api/config/probe-postgres-backup.env.example": strings.Join([]string{
			"PGHOST=127.0.0.1", "PGPORT=5432", "PGDATABASE=probe", "PGUSER=probe",
			"PGPASSFILE=/srv/probe/config/probe-postgres.pgpass", "PROBE_POSTGRES_BACKUP_DIR=/var/backups/probe-panel/postgres", "",
		}, "\n"),
	}
	for relative, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func writeTestCertificate(root string, domains []string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: domains[0]},
		DNSNames: domains, NotBefore: time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
		NotAfter: time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC), KeyUsage: x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return err
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	key := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(root, "fullchain.pem"), certificate, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "privkey.pem"), key, 0o600)
}
