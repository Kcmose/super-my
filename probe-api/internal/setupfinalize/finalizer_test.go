package setupfinalize

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"probe-api/internal/setup"
)

const testPlatformID = PlatformDebian13Systemd

func testPlatformContract(t *testing.T, platformID string) platformContract {
	t.Helper()
	contract, err := platformContractFor(platformID)
	if err != nil {
		t.Fatal(err)
	}
	return contract
}

func acceptTestHostPlatform(context.Context, Runner, string, string) error {
	return nil
}

func TestFinalizerRejectsHostPlatformDriftBeforeOwnership(t *testing.T) {
	base := t.TempDir()
	bundle := filepath.Join(base, "bundle")
	writeTestBundle(t, bundle)
	runner := &fakeRunner{}
	identityLookups := 0
	platformChecks := 0
	finalizer, err := New(Config{
		BundleRoot: bundle, ReleaseID: "v1.2.0", PlatformID: PlatformUbuntu2204Systemd,
		Paths: testPaths(base), Runner: runner,
		IdentityLookup: func(string) (Identity, error) {
			identityLookups++
			return Identity{UID: os.Getuid(), GID: os.Getgid()}, nil
		},
		RequireRoot: false,
		ValidateHostPlatform: func(context.Context, Runner, string, string) error {
			platformChecks++
			return errors.New("simulated platform drift")
		},
		CommitInstalled: func(time.Time) error {
			t.Fatal("platform drift reached installed commit")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := setup.CompleteRequest{
		Profile: setup.InstallationProfileManagement,
		Database: setup.DatabaseInput{
			Mode: "local", Name: "probe", Username: "probe",
			Password: setup.Secret("db-secret-1234"), PasswordConfirmation: setup.Secret("db-secret-1234"),
		},
		Network: setup.NetworkInput{Address: "10.20.30.40"},
		TLS:     setup.TLSInput{Mode: "private_ca"}, Allowlist: []string{"10.20.30.0/24"},
		Administrator: setup.AdministratorInput{
			Username: "admin", Password: setup.Secret("admin-secret-1234"), PasswordConfirmation: setup.Secret("admin-secret-1234"),
		},
	}
	err = finalizer.Finalize(context.Background(), request)
	if !errors.Is(err, ErrPreflight) || platformChecks != 1 {
		t.Fatalf("platform drift error = %v, checks = %d", err, platformChecks)
	}
	if identityLookups != 0 || len(runner.commands) != 0 {
		t.Fatalf("platform drift mutated or crossed host preflight: identities=%d commands=%v", identityLookups, runner.commands)
	}
}

func TestHostPlatformValidatorUsesReadOnlyReleaseCheck(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "bundle")
	script := filepath.Join(bundle, "source", "probe-api", "deploy", "scripts", "install-release.sh")
	want := script + " --bundle-root " + bundle + " --check-platform " + PlatformDebian12Systemd
	runner := &fakeRunner{}
	if err := validateHostPlatform(context.Background(), runner, bundle, PlatformDebian12Systemd); err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) != 1 || runner.commands[0] != want {
		t.Fatalf("platform validation commands = %v; want %q", runner.commands, want)
	}
	runner = &fakeRunner{commandErrors: map[string]error{want: errors.New("mismatch")}}
	if err := validateHostPlatform(context.Background(), runner, bundle, PlatformDebian12Systemd); err == nil {
		t.Fatal("host platform mismatch was accepted")
	}
}

type fakeRunner struct {
	certificateRoot       string
	domains               []string
	listeners             []byte
	listenerError         error
	postgresVersion       []byte
	commands              []string
	sensitiveInputs       [][]byte
	commandErrors         map[string]error
	commandErrorSequences map[string][]error
	outputValues          map[string][]byte
	onRun                 func(string)
}

func (runner *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	command := name + " " + strings.Join(args, " ")
	runner.commands = append(runner.commands, command)
	if runner.onRun != nil {
		runner.onRun(command)
	}
	if sequence := runner.commandErrorSequences[command]; len(sequence) > 0 {
		err := sequence[0]
		runner.commandErrorSequences[command] = sequence[1:]
		if err != nil {
			return err
		}
	}
	if err := runner.commandErrors[command]; err != nil {
		return err
	}
	if filepathBase(name) == "certbot" {
		return writeTestCertificate(runner.certificateRoot, runner.domains)
	}
	return nil
}

func (runner *fakeRunner) RunQuiet(_ context.Context, name string, args ...string) error {
	command := name + " " + strings.Join(args, " ")
	runner.commands = append(runner.commands, command)
	return runner.commandErrors[command]
}

func (runner *fakeRunner) RunSensitive(_ context.Context, stdin []byte, name string, args ...string) error {
	command := name + " " + strings.Join(args, " ")
	runner.commands = append(runner.commands, command)
	runner.sensitiveInputs = append(runner.sensitiveInputs, append([]byte(nil), stdin...))
	return runner.commandErrors[command]
}

func (runner *fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	command := name + " " + strings.Join(args, " ")
	runner.commands = append(runner.commands, command)
	if err := runner.commandErrors[command]; err != nil {
		return nil, err
	}
	if value, ok := runner.outputValues[command]; ok {
		return append([]byte(nil), value...), nil
	}
	if filepathBase(name) == "ss" {
		if runner.listenerError != nil {
			return nil, runner.listenerError
		}
		if runner.listeners != nil {
			return append([]byte(nil), runner.listeners...), nil
		}
		return []byte("LISTEN 0 128 127.0.0.1:5432 0.0.0.0:*\n"), nil
	}
	if filepathBase(name) == "env" {
		if strings.Contains(strings.Join(args, " "), "SHOW server_version_num;") {
			if runner.postgresVersion != nil {
				return append([]byte(nil), runner.postgresVersion...), nil
			}
			return []byte("140000\n"), nil
		}
		return []byte("no\n"), nil
	}
	if filepathBase(name) == "systemctl" && len(args) == 3 && args[0] == "show" {
		if args[1] == "--property=ActiveState" {
			return []byte("ActiveState=inactive\n"), nil
		}
		if args[1] == "--property=UnitFileState" {
			return []byte("UnitFileState=disabled\n"), nil
		}
	}
	return nil, nil
}

type fakeBootstrapper struct {
	databasePassword string
	adminPassword    string
	called           bool
	onCall           func()
}

func (bootstrapper *fakeBootstrapper) MigrateAndBootstrap(_ context.Context, database DatabaseConfig, _ string, administratorPassword []byte) error {
	if bootstrapper.onCall != nil {
		bootstrapper.onCall()
	}
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
	if err := os.Chmod(filepath.Dir(paths.Allowlist), 0o755); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(base, "bundle")
	writeTestBundle(t, bundle)
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	domains := []string{"panel.monitor.test", "admin.monitor.test", "api.monitor.test"}
	runner := &fakeRunner{certificateRoot: paths.LetsEncryptLive, domains: domains}
	bootstrapper := &fakeBootstrapper{}
	commandsAtBootstrap := -1
	bootstrapper.onCall = func() { commandsAtBootstrap = len(runner.commands) }
	currentIdentity := Identity{UID: os.Getuid(), GID: os.Getgid()}
	installedCommits := 0
	commandsAtCommit := 0
	finalizer, err := New(Config{
		BundleRoot: bundle, ReleaseID: "v1.0.0", PlatformID: testPlatformID, Paths: paths,
		Runner: runner, Bootstrapper: bootstrapper,
		IdentityLookup: func(string) (Identity, error) { return currentIdentity, nil },
		RootIdentity:   currentIdentity,
		RequireRoot:    false, Now: func() time.Time { return now },
		ValidateHostPlatform: acceptTestHostPlatform,
		ResolveHostname:      func(context.Context, string) error { return nil },
		CommitInstalled: func(commitTime time.Time) error {
			installedCommits++
			commandsAtCommit = len(runner.commands)
			if !commitTime.Equal(now) {
				t.Errorf("installed commit time = %s; want %s", commitTime, now)
			}
			return nil
		},
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
	if installedCommits != 1 || commandsAtCommit != len(runner.commands) {
		t.Fatalf("installed commits = %d, commands at commit = %d, final commands = %d", installedCommits, commandsAtCommit, len(runner.commands))
	}
	versionGateIndex := commandIndexContaining(runner.commands, "SHOW server_version_num;")
	certificateIndex := commandIndexContaining(runner.commands, "/usr/bin/certbot certonly")
	timerEnableIndex := commandIndexContaining(runner.commands, "/usr/bin/systemctl enable certbot.timer")
	timerStartIndex := commandIndexContaining(runner.commands, "/usr/bin/systemctl start certbot.timer")
	tlsValidationIndex := commandIndexContaining(runner.commands, filepath.Join(bundle, "artifacts", "api", "probe-api")+" config validate-ingress-tls domain "+strings.Join(domains, " "))
	activateIndex := commandIndexContaining(runner.commands, "/deploy/scripts/install-release.sh --bundle-root")
	disableSetupIndex := commandIndexContaining(runner.commands, "/usr/bin/systemctl disable probe-panel-setup.service")
	stopPathIndex := commandIndexContaining(runner.commands, "/usr/bin/systemctl stop probe-panel-finalizer.path")
	if versionGateIndex < 0 || certificateIndex <= versionGateIndex || tlsValidationIndex <= certificateIndex || timerEnableIndex <= tlsValidationIndex || timerStartIndex <= timerEnableIndex || commandsAtBootstrap <= timerStartIndex || activateIndex != commandsAtBootstrap || disableSetupIndex <= activateIndex || stopPathIndex <= disableSetupIndex {
		t.Fatalf("certificate/timer/TLS validation/bootstrap/activation/terminal ordering is invalid: bootstrap=%d commands=%v", commandsAtBootstrap, runner.commands)
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
	if err != nil || !strings.Contains(string(apiEnvironment), "PROBE_PLATFORM_ID="+testPlatformID) || !strings.Contains(string(apiEnvironment), "PROBE_INGRESS_MODE=domain") || !strings.Contains(string(apiEnvironment), "PROBE_ADMIN_ORIGIN=https://admin.monitor.test") {
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

func TestFinalizerCompletesPrivateCAIPInstallationWithoutDNSOrACME(t *testing.T) {
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
	if err := os.Chmod(filepath.Dir(paths.Allowlist), 0o755); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(base, "bundle")
	writeTestBundle(t, bundle)
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	runner := &fakeRunner{listeners: []byte(strings.Join([]string{
		"LISTEN 0 128 127.0.0.1:5432 0.0.0.0:*",
		"LISTEN 0 128 0.0.0.0:80 0.0.0.0:*",
		"LISTEN 0 128 [::]:443 [::]:*", "",
	}, "\n"))}
	bootstrapper := &fakeBootstrapper{}
	commandsAtBootstrap := -1
	bootstrapper.onCall = func() { commandsAtBootstrap = len(runner.commands) }
	currentIdentity := Identity{UID: os.Getuid(), GID: os.Getgid()}
	resolveCalls := 0
	installedCommits := 0
	commandsAtCommit := 0
	finalizer, err := New(Config{
		BundleRoot: bundle, ReleaseID: "v1.0.0", PlatformID: testPlatformID, Paths: paths,
		Runner: runner, Bootstrapper: bootstrapper,
		IdentityLookup: func(string) (Identity, error) { return currentIdentity, nil },
		RootIdentity:   currentIdentity, RequireRoot: false, Now: func() time.Time { return now },
		ValidateHostPlatform: acceptTestHostPlatform,
		ResolveHostname: func(context.Context, string) error {
			resolveCalls++
			return nil
		},
		CommitInstalled: func(time.Time) error {
			installedCommits++
			commandsAtCommit = len(runner.commands)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := setup.CompleteRequest{
		Database: setup.DatabaseInput{
			Mode: "local", Name: "probe", Username: "probe",
			Password: setup.Secret("db-secret-1234"), PasswordConfirmation: setup.Secret("db-secret-1234"),
		},
		Domains: setup.DomainInput{}, Network: setup.NetworkInput{Address: "10.20.30.40"},
		TLS:       setup.TLSInput{Mode: "private_ca", Email: ""},
		Allowlist: []string{"10.20.30.0/24"},
		Administrator: setup.AdministratorInput{
			Username: "admin", Password: setup.Secret("admin-secret-1234"), PasswordConfirmation: setup.Secret("admin-secret-1234"),
		},
	}
	if err := finalizer.Finalize(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if installedCommits != 1 || commandsAtCommit != len(runner.commands) {
		t.Fatalf("installed commits = %d, commands at commit = %d, final commands = %d", installedCommits, commandsAtCommit, len(runner.commands))
	}
	if resolveCalls != 0 {
		t.Fatalf("IP installation performed %d DNS lookups", resolveCalls)
	}
	commands := strings.Join(runner.commands, "\n")
	if strings.Contains(commands, "/usr/bin/certbot ") || strings.Contains(commands, "systemctl enable certbot.timer") {
		t.Fatalf("IP installation invoked ACME provisioning: %s", commands)
	}
	for _, required := range []string{
		"systemctl stop certbot.timer",
		"systemctl disable certbot.timer",
		"systemctl disable probe-panel-setup.service probe-panel-setup.socket probe-panel-finalizer.path",
		"systemctl stop probe-panel-finalizer.path",
	} {
		if !strings.Contains(commands, required) {
			t.Fatalf("IP installation did not run %q: %s", required, commands)
		}
	}
	if strings.Contains(commands, "systemctl stop probe-panel-setup.socket") {
		t.Fatal("Finalizer stopped the setup socket before the installed result could be observed")
	}
	postgresStartIndex := commandIndexContaining(runner.commands, "/usr/bin/systemctl start postgresql.service")
	postgresActiveIndex := commandIndexContaining(runner.commands, "/usr/bin/systemctl is-active --quiet postgresql.service")
	timerStopIndex := commandIndexContaining(runner.commands, "/usr/bin/systemctl stop certbot.timer")
	timerDisableIndex := commandIndexContaining(runner.commands, "/usr/bin/systemctl disable certbot.timer")
	tlsValidationIndex := commandIndexContaining(runner.commands, filepath.Join(bundle, "artifacts", "api", "probe-api")+" config validate-ingress-tls ip 10.20.30.40")
	activateIndex := commandIndexContaining(runner.commands, "/deploy/scripts/install-release.sh --bundle-root")
	if postgresStartIndex < 0 || postgresActiveIndex <= postgresStartIndex || tlsValidationIndex < 0 || timerStopIndex <= tlsValidationIndex || timerDisableIndex <= timerStopIndex || commandsAtBootstrap <= timerDisableIndex || activateIndex != commandsAtBootstrap || strings.Contains(commands, "enable --now postgresql.service") {
		t.Fatalf("PostgreSQL/timer/TLS validation/bootstrap/activation ordering is invalid: bootstrap=%d commands=%s", commandsAtBootstrap, commands)
	}

	apiEnvironment, err := os.ReadFile(paths.APIEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"PROBE_PLATFORM_ID=" + testPlatformID,
		"PROBE_INGRESS_MODE=ip",
		"PROBE_ADMIN_ORIGIN=https://10.20.30.40:18455",
		"PROBE_AGENT_PUBLIC_URL=https://10.20.30.40:18454",
		"PROBE_AGENT_INSTALL_CA_FILE=" + paths.PrivateCACertificate,
	} {
		if !strings.Contains(string(apiEnvironment), expected) {
			t.Fatalf("API environment is missing %q", expected)
		}
	}
	nginxConfig, err := os.ReadFile(paths.ActiveNginxConfig)
	if err != nil || strings.Contains(string(nginxConfig), "PROBE_SETUP_SERVER_IP") {
		t.Fatalf("IP Nginx config was not rendered: %v", err)
	}
	for _, hostPort := range []string{"10.20.30.40:18453", "10.20.30.40:18454", "10.20.30.40:18455"} {
		if !strings.Contains(string(nginxConfig), hostPort) {
			t.Fatalf("IP Nginx config is missing %s", hostPort)
		}
	}

	caContents, err := os.ReadFile(paths.PrivateCACertificate)
	if err != nil {
		t.Fatal(err)
	}
	caBlock, caRest := pem.Decode(caContents)
	if caBlock == nil || caBlock.Type != "CERTIFICATE" || len(caRest) != 0 {
		t.Fatal("public CA file is not exactly one PEM certificate")
	}
	caCertificate, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil || !caCertificate.IsCA {
		t.Fatalf("generated CA certificate is invalid: %v", err)
	}
	leafContents, err := os.ReadFile(paths.PrivateCertificate)
	if err != nil {
		t.Fatal(err)
	}
	leafBlock, _ := pem.Decode(leafContents)
	if leafBlock == nil {
		t.Fatal("generated IP leaf certificate is missing")
	}
	leafCertificate, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatalf("parse generated IP leaf: %v", err)
	}
	if err := leafCertificate.VerifyHostname("10.20.30.40"); err != nil {
		t.Fatalf("generated leaf does not cover the configured IP: %v", err)
	}
	for path, wantMode := range map[string]os.FileMode{
		paths.Allowlist:            0o640,
		paths.PrivateCACertificate: 0o644,
		paths.PrivateCertificate:   0o644,
		paths.PrivateCAKey:         0o600,
		paths.PrivateKey:           0o600,
	} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", path, statErr)
		}
		if info.Mode().Perm() != wantMode {
			t.Fatalf("%s mode = %v; want %v", path, info.Mode().Perm(), wantMode)
		}
	}
	panelConfigDirectory, err := os.Stat(filepath.Dir(paths.Allowlist))
	if err != nil {
		t.Fatal(err)
	}
	if panelConfigDirectory.Mode().Perm() != 0o755 {
		t.Fatalf("public CA parent mode = %v; want 0755", panelConfigDirectory.Mode().Perm())
	}
}

func TestManagementIPFinalizerCoexistsAndInstallsOnlyAdministratorIngress(t *testing.T) {
	base := t.TempDir()
	paths := testPaths(base)
	for _, directory := range []string{
		filepath.Join(base, "srv"), filepath.Join(base, "var", "backups"),
		filepath.Join(base, "etc"), filepath.Join(base, "etc", "probe-panel"),
		paths.NginxConfD, paths.NginxSitesEnabled, filepath.Dir(paths.APIUnit),
		filepath.Dir(paths.NginxNativeUnit), filepath.Dir(paths.NginxWantsLink),
		paths.ProbeRoot, paths.AgentPath, paths.WebPath,
	} {
		mkdirAllWithMode(t, directory, 0o755)
	}
	if err := os.WriteFile(paths.NginxNativeUnit, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(paths.NginxNativeUnit, paths.NginxWantsLink); err != nil {
		t.Fatal(err)
	}
	foreignConfig := filepath.Join(paths.NginxConfD, "existing-site.conf")
	if err := os.WriteFile(foreignConfig, []byte("# existing site\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agentSentinel := filepath.Join(paths.AgentPath, "keep")
	webSentinel := filepath.Join(paths.WebPath, "keep")
	for _, sentinel := range []string{agentSentinel, webSentinel} {
		if err := os.WriteFile(sentinel, []byte("existing\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	bundle := filepath.Join(base, "bundle")
	writeTestBundle(t, bundle)
	runner := &fakeRunner{listeners: []byte(strings.Join([]string{
		"LISTEN 0 128 127.0.0.1:5432 0.0.0.0:*",
		"LISTEN 0 128 0.0.0.0:18453 0.0.0.0:*",
		"LISTEN 0 128 [::]:18454 [::]:*", "",
	}, "\n")), outputValues: map[string][]byte{
		"/usr/sbin/nginx -T": []byte("include " + filepath.Join(paths.NginxConfD, "*.conf") + ";\n"),
	}}
	bootstrapper := &fakeBootstrapper{}
	identity := Identity{UID: os.Getuid(), GID: os.Getgid()}
	resolveCalls := 0
	finalizer, err := New(Config{
		BundleRoot: bundle, ReleaseID: "v1.1.0-management", PlatformID: PlatformUbuntu2404Systemd, Paths: paths,
		Runner: runner, Bootstrapper: bootstrapper,
		IdentityLookup: func(string) (Identity, error) { return identity, nil },
		RootIdentity:   identity, RequireRoot: false,
		Now:                  func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) },
		ValidateHostPlatform: acceptTestHostPlatform,
		ResolveHostname: func(context.Context, string) error {
			resolveCalls++
			return nil
		},
		CommitInstalled: func(time.Time) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := setup.CompleteRequest{
		Profile: setup.InstallationProfileManagement,
		Database: setup.DatabaseInput{
			Mode: "local", Name: "probe", Username: "probe",
			Password: setup.Secret("db-secret-1234"), PasswordConfirmation: setup.Secret("db-secret-1234"),
		},
		Domains: setup.DomainInput{}, Network: setup.NetworkInput{Address: "10.20.30.40"},
		TLS: setup.TLSInput{Mode: "private_ca"}, Allowlist: []string{"10.20.30.0/24"},
		Administrator: setup.AdministratorInput{
			Username: "admin", Password: setup.Secret("admin-secret-1234"), PasswordConfirmation: setup.Secret("admin-secret-1234"),
		},
	}
	if err := finalizer.Finalize(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if resolveCalls != 0 || !bootstrapper.called {
		t.Fatalf("management IP DNS calls = %d, bootstrap = %v", resolveCalls, bootstrapper.called)
	}
	commands := strings.Join(runner.commands, "\n")
	for _, forbidden := range []string{
		"certbot.timer", "/usr/bin/certbot ", "systemctl stop nginx.service", "systemctl disable nginx.service",
		"--disable-default-site", "validate-ingress-tls ip 10.20.30.40",
	} {
		if strings.Contains(commands, forbidden) {
			t.Fatalf("management IP installation ran forbidden command %q: %s", forbidden, commands)
		}
	}
	for _, required := range []string{
		"config validate-ingress-tls admin-ip 10.20.30.40",
		"--profile management",
	} {
		if !strings.Contains(commands, required) {
			t.Fatalf("management IP installation is missing %q: %s", required, commands)
		}
	}

	apiEnvironment, err := os.ReadFile(paths.APIEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"PROBE_PLATFORM_ID=" + PlatformUbuntu2404Systemd,
		"PROBE_INSTALLATION_PROFILE=management",
		"PROBE_ADMIN_ORIGIN=https://10.20.30.40:18455",
		"PROBE_AGENT_PUBLIC_URL=\n",
		"PROBE_AGENT_INSTALLER_URL=\n",
		"PROBE_AGENT_INSTALL_CA_FILE=\n",
	} {
		if !strings.Contains(string(apiEnvironment), expected) {
			t.Fatalf("management API environment is missing %q", expected)
		}
	}
	nginxConfig, err := os.ReadFile(paths.ActiveNginxConfig)
	if err != nil || !strings.Contains(string(nginxConfig), "10.20.30.40:18455") || strings.Contains(string(nginxConfig), "18453") || strings.Contains(string(nginxConfig), "18454") {
		t.Fatalf("management IP Nginx config = %q, error = %v", nginxConfig, err)
	}
	for _, preserved := range []string{foreignConfig, paths.NginxWantsLink, agentSentinel, webSentinel} {
		if _, err := os.Lstat(preserved); err != nil {
			t.Fatalf("coexisting asset %s was not preserved: %v", preserved, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(paths.TLSRoot, "admin", "fullchain.pem")); err != nil {
		t.Fatalf("management TLS link is missing: %v", err)
	}
	for _, absent := range []string{filepath.Join(paths.TLSRoot, "panel"), filepath.Join(paths.TLSRoot, "api")} {
		if _, err := os.Lstat(absent); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("independent surface TLS path exists: %s (%v)", absent, err)
		}
	}
	backupParentInfo, err := os.Stat(filepath.Dir(paths.PostgresBackupDir))
	if err != nil || backupParentInfo.Mode().Perm() != 0o710 {
		t.Fatalf("PostgreSQL backup parent mode = %v, error = %v", backupParentInfo, err)
	}
	backupInfo, err := os.Stat(paths.PostgresBackupDir)
	if err != nil || backupInfo.Mode().Perm() != 0o700 {
		t.Fatalf("PostgreSQL backup directory mode = %v, error = %v", backupInfo, err)
	}
}

func TestInstalledCommitFailureRollsBackAndVerifiesEveryFormalUnit(t *testing.T) {
	base := t.TempDir()
	paths := testPaths(base)
	for _, directory := range []string{
		filepath.Join(base, "srv"), filepath.Join(base, "var", "backups"),
		filepath.Join(base, "etc"), filepath.Join(base, "etc", "probe-panel"),
		paths.NginxConfD, paths.NginxSitesEnabled, filepath.Dir(paths.APIUnit),
	} {
		mkdirAllWithMode(t, directory, 0o755)
	}
	bundle := filepath.Join(base, "bundle")
	writeTestBundle(t, bundle)
	stopAPICommand := "/usr/bin/systemctl stop probe-api.service"
	nginxStateCommand := "/usr/bin/systemctl show --property=ActiveState nginx.service"
	runner := &fakeRunner{
		listeners:     []byte("LISTEN 0 128 127.0.0.1:5432 0.0.0.0:*\n"),
		commandErrors: map[string]error{stopAPICommand: errors.New("simulated stop failure")},
		outputValues:  map[string][]byte{nginxStateCommand: []byte("ActiveState=active\n")},
	}
	bootstrapper := &fakeBootstrapper{}
	currentIdentity := Identity{UID: os.Getuid(), GID: os.Getgid()}
	commandsAtCommit := -1
	var wantsLinkSetupError error
	finalizer, err := New(Config{
		BundleRoot: bundle, ReleaseID: "v1.0.0", PlatformID: testPlatformID, Paths: paths,
		Runner: runner, Bootstrapper: bootstrapper,
		IdentityLookup: func(string) (Identity, error) { return currentIdentity, nil },
		RootIdentity:   currentIdentity, RequireRoot: false,
		Now:                  func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) },
		ValidateHostPlatform: acceptTestHostPlatform,
		ResolveHostname:      func(context.Context, string) error { return nil },
		CommitInstalled: func(time.Time) error {
			commandsAtCommit = len(runner.commands)
			if mkdirErr := os.MkdirAll(filepath.Dir(paths.NginxNativeUnit), 0o755); mkdirErr != nil {
				wantsLinkSetupError = mkdirErr
			} else if writeErr := os.WriteFile(paths.NginxNativeUnit, []byte("[Unit]\n"), 0o644); writeErr != nil {
				wantsLinkSetupError = writeErr
			} else if mkdirErr := os.MkdirAll(filepath.Dir(paths.NginxWantsLink), 0o755); mkdirErr != nil {
				wantsLinkSetupError = mkdirErr
			} else {
				wantsLinkSetupError = os.Symlink(paths.NginxNativeUnit, paths.NginxWantsLink)
			}
			return errors.New("simulated durable state failure")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := setup.CompleteRequest{
		Database: setup.DatabaseInput{
			Mode: "local", Name: "probe", Username: "probe",
			Password: setup.Secret("db-secret-1234"), PasswordConfirmation: setup.Secret("db-secret-1234"),
		},
		Network: setup.NetworkInput{Address: "10.20.30.40"},
		TLS:     setup.TLSInput{Mode: "private_ca"}, Allowlist: []string{"10.20.30.0/24"},
		Administrator: setup.AdministratorInput{
			Username: "admin", Password: setup.Secret("admin-secret-1234"), PasswordConfirmation: setup.Secret("admin-secret-1234"),
		},
	}
	err = finalizer.Finalize(context.Background(), request)
	if wantsLinkSetupError != nil {
		t.Fatalf("simulate add-wants relationship: %v", wantsLinkSetupError)
	}
	if err == nil || !strings.Contains(err.Error(), "commit installed setup state") || !strings.Contains(err.Error(), "simulated stop failure") || !strings.Contains(err.Error(), "nginx.service remains active") {
		t.Fatalf("commit failure did not include verified rollback failure: %v", err)
	}
	if commandsAtCommit < 0 || commandsAtCommit >= len(runner.commands) || runner.commands[commandsAtCommit] != stopAPICommand {
		t.Fatalf("rollback did not begin immediately after the final commit attempt: commit=%d commands=%v", commandsAtCommit, runner.commands)
	}
	for _, required := range []string{
		"/usr/bin/systemctl stop probe-api.service",
		"/usr/bin/systemctl stop probe-postgres-backup.timer",
		"/usr/bin/systemctl stop nginx.service",
		"/usr/bin/systemctl stop certbot.timer",
		"/usr/bin/systemctl disable probe-api.service",
		"/usr/bin/systemctl disable probe-postgres-backup.timer",
		"/usr/bin/systemctl disable certbot.timer",
		"/usr/bin/systemctl daemon-reload",
		"/usr/bin/systemctl show --property=ActiveState probe-api.service",
		"/usr/bin/systemctl show --property=ActiveState probe-postgres-backup.timer",
		"/usr/bin/systemctl show --property=ActiveState nginx.service",
		"/usr/bin/systemctl show --property=ActiveState certbot.timer",
		"/usr/bin/systemctl show --property=UnitFileState nginx.service",
	} {
		if commandIndexContaining(runner.commands[commandsAtCommit:], required) < 0 {
			t.Fatalf("rollback omitted or prematurely ran %q: %v", required, runner.commands)
		}
	}
	commands := strings.Join(runner.commands, "\n")
	if strings.Contains(commands, "disable --now probe-api.service") || strings.Contains(commands, "disable nginx.service") || strings.Contains(commands, "remove-wants") {
		t.Fatalf("rollback invoked an unsafe combined/SysV disable path: %s", commands)
	}
	if _, statErr := os.Lstat(paths.NginxWantsLink); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("reviewed nginx wants link remains after rollback: %v", statErr)
	}
}

func TestManagementNginxConflictFailsBeforeDatabaseCreation(t *testing.T) {
	base := t.TempDir()
	paths := testPaths(base)
	for _, directory := range []string{
		filepath.Join(base, "srv"), filepath.Join(base, "var", "backups"),
		filepath.Join(base, "etc"), filepath.Join(base, "etc", "probe-panel"),
		paths.NginxConfD, paths.NginxSitesEnabled, filepath.Dir(paths.APIUnit),
	} {
		mkdirAllWithMode(t, directory, 0o755)
	}
	bundle := filepath.Join(base, "bundle")
	writeTestBundle(t, bundle)
	runner := &fakeRunner{
		listeners: []byte("LISTEN 0 128 127.0.0.1:5432 0.0.0.0:*\n"),
		commandErrors: map[string]error{
			"/usr/sbin/nginx -t": errors.New("simulated shared configuration conflict"),
		},
		outputValues: map[string][]byte{
			"/usr/sbin/nginx -T": []byte("include " + filepath.Join(paths.NginxConfD, "*.conf") + ";\n"),
		},
	}
	bootstrapper := &fakeBootstrapper{}
	identity := Identity{UID: os.Getuid(), GID: os.Getgid()}
	finalizer, err := New(Config{
		BundleRoot: bundle, ReleaseID: "v1.2.0", PlatformID: testPlatformID, Paths: paths,
		Runner: runner, Bootstrapper: bootstrapper,
		IdentityLookup: func(string) (Identity, error) { return identity, nil },
		RootIdentity:   identity, RequireRoot: false,
		Now:                  func() time.Time { return time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC) },
		ValidateHostPlatform: acceptTestHostPlatform,
		ResolveHostname:      func(context.Context, string) error { return nil },
		CommitInstalled:      func(time.Time) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := setup.CompleteRequest{
		Profile: setup.InstallationProfileManagement,
		Database: setup.DatabaseInput{
			Mode: "local", Name: "probe", Username: "probe",
			Password: setup.Secret("db-secret-1234"), PasswordConfirmation: setup.Secret("db-secret-1234"),
		},
		Domains: setup.DomainInput{}, Network: setup.NetworkInput{Address: "10.20.30.40"},
		TLS: setup.TLSInput{Mode: "private_ca"}, Allowlist: []string{"10.20.30.0/24"},
		Administrator: setup.AdministratorInput{
			Username: "admin", Password: setup.Secret("admin-secret-1234"), PasswordConfirmation: setup.Secret("admin-secret-1234"),
		},
	}
	err = finalizer.Finalize(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "validate management Nginx candidate") {
		t.Fatalf("management Nginx conflict was not reported: %v", err)
	}
	if bootstrapper.called || len(runner.sensitiveInputs) != 0 {
		t.Fatalf("database was initialized before Nginx compatibility was proven: bootstrap=%v SQL inputs=%d", bootstrapper.called, len(runner.sensitiveInputs))
	}
	if _, statErr := os.Lstat(paths.NginxLink); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("temporary management Nginx candidate remains after conflict: %v", statErr)
	}
}

func TestManagementNginxCandidateCleanupRestoresPreexistingActiveNginx(t *testing.T) {
	paths := testPaths(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(paths.ActiveNginxConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.NginxConfD, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ActiveNginxConfig, []byte("# generated management config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nginxTestCalls := 0
	var observationFailures []string
	runner := &fakeRunner{}
	runner.onRun = func(command string) {
		switch command {
		case "/usr/sbin/nginx -t":
			nginxTestCalls++
			info, err := os.Lstat(paths.NginxLink)
			if nginxTestCalls == 1 {
				if err != nil || info.Mode()&os.ModeSymlink == 0 {
					observationFailures = append(observationFailures, fmt.Sprintf("candidate was not linked during combined Nginx validation: info=%v error=%v", info, err))
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				observationFailures = append(observationFailures, fmt.Sprintf("candidate remained linked during shared Nginx restoration: %v", err))
			}
		case "/usr/bin/systemctl reload nginx.service":
			if _, err := os.Lstat(paths.NginxLink); !errors.Is(err, os.ErrNotExist) {
				observationFailures = append(observationFailures, fmt.Sprintf("candidate remained linked when shared Nginx was reloaded: %v", err))
			}
		}
	}
	finalizer := &Finalizer{
		config:                       Config{Paths: paths, Runner: runner},
		managementNginxStateCaptured: true,
		managementNginxWasActive:     true,
	}
	if err := finalizer.validateManagementNginxCandidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(observationFailures) != 0 {
		t.Fatalf("candidate link lifecycle observations failed: %v", observationFailures)
	}
	if _, err := os.Lstat(paths.NginxLink); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary management Nginx candidate remains after validation: %v", err)
	}
	want := strings.Join([]string{
		"/usr/sbin/nginx -t",
		"/usr/sbin/nginx -t",
		"/usr/bin/systemctl reload nginx.service",
	}, "\n")
	if got := strings.Join(runner.commands, "\n"); got != want {
		t.Fatalf("candidate cleanup commands = %q; want %q", got, want)
	}
}

func TestManagementNginxCandidateCleanupDoesNotStartInactiveNginx(t *testing.T) {
	paths := testPaths(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(paths.ActiveNginxConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.NginxConfD, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ActiveNginxConfig, []byte("# generated management config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	finalizer := &Finalizer{
		config:                       Config{Paths: paths, Runner: runner},
		managementNginxStateCaptured: true,
		managementNginxWasActive:     false,
	}
	if err := finalizer.validateManagementNginxCandidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(runner.commands, "\n"); got != "/usr/sbin/nginx -t" {
		t.Fatalf("inactive Nginx candidate cleanup ran unexpected commands: %s", got)
	}
}

func TestManagementNginxCandidateValidationFailureStillRestoresActiveNginx(t *testing.T) {
	paths := testPaths(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(paths.ActiveNginxConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.NginxConfD, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ActiveNginxConfig, []byte("# generated management config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nginxTestCommand := "/usr/sbin/nginx -t"
	runner := &fakeRunner{
		commandErrorSequences: map[string][]error{
			nginxTestCommand: []error{errors.New("simulated candidate conflict"), nil},
		},
	}
	nginxTestCalls := 0
	var observationFailures []string
	runner.onRun = func(command string) {
		if command != nginxTestCommand && command != "/usr/bin/systemctl reload nginx.service" {
			return
		}
		if command == nginxTestCommand {
			nginxTestCalls++
			if nginxTestCalls == 1 {
				if info, err := os.Lstat(paths.NginxLink); err != nil || info.Mode()&os.ModeSymlink == 0 {
					observationFailures = append(observationFailures, fmt.Sprintf("failed candidate was not linked during combined Nginx validation: info=%v error=%v", info, err))
				}
				return
			}
		}
		if _, err := os.Lstat(paths.NginxLink); !errors.Is(err, os.ErrNotExist) {
			observationFailures = append(observationFailures, fmt.Sprintf("failed candidate remained linked during shared Nginx restoration: %v", err))
		}
	}
	finalizer := &Finalizer{
		config:                       Config{Paths: paths, Runner: runner},
		managementNginxStateCaptured: true,
		managementNginxWasActive:     true,
	}
	err := finalizer.validateManagementNginxCandidate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "validate management Nginx candidate") {
		t.Fatalf("candidate validation conflict was not preserved: %v", err)
	}
	if len(observationFailures) != 0 {
		t.Fatalf("failed candidate link lifecycle observations failed: %v", observationFailures)
	}
	if _, statErr := os.Lstat(paths.NginxLink); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("temporary management Nginx candidate remains after failed validation: %v", statErr)
	}
	want := strings.Join([]string{
		nginxTestCommand,
		nginxTestCommand,
		"/usr/bin/systemctl reload nginx.service",
	}, "\n")
	if got := strings.Join(runner.commands, "\n"); got != want {
		t.Fatalf("failed candidate cleanup commands = %q; want %q", got, want)
	}
}

func TestManagementRollbackRevalidatesActiveNginxAfterCandidateLinkWasAlreadyRemoved(t *testing.T) {
	paths := testPaths(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(paths.ActiveNginxConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.NginxConfD, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ActiveNginxConfig, []byte("# generated management config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	finalizer := &Finalizer{
		config:                       Config{Paths: paths, Runner: runner},
		managementNginxStateCaptured: true,
		managementNginxWasActive:     true,
	}
	if err := finalizer.validateManagementNginxCandidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	commandsBeforeRollback := len(runner.commands)
	if err := finalizer.stopProductionForProfile(setup.InstallationProfileManagement, true); err != nil {
		t.Fatal(err)
	}
	commands := strings.Join(runner.commands[commandsBeforeRollback:], "\n")
	validateIndex := strings.Index(commands, "/usr/sbin/nginx -t")
	reloadIndex := strings.Index(commands, "/usr/bin/systemctl reload nginx.service")
	if validateIndex < 0 || reloadIndex <= validateIndex {
		t.Fatalf("rollback did not revalidate and reload active Nginx after candidate cleanup: %s", commands)
	}
	if strings.Contains(commands, "/usr/bin/systemctl stop nginx.service") {
		t.Fatalf("rollback stopped a preexisting active shared Nginx service: %s", commands)
	}
}

func TestManagementNginxEnablementRejectsUnsafeWantsPathBeforeOwnership(t *testing.T) {
	for name, createUnsafePath := range map[string]func(t *testing.T, paths Paths){
		"regular file": func(t *testing.T, paths Paths) {
			t.Helper()
			if err := os.WriteFile(paths.NginxWantsLink, []byte("not a symlink\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"unexpected symlink target": func(t *testing.T, paths Paths) {
			t.Helper()
			unexpectedUnit := filepath.Join(filepath.Dir(paths.NginxNativeUnit), "unexpected.service")
			if err := os.WriteFile(unexpectedUnit, []byte("[Unit]\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(unexpectedUnit, paths.NginxWantsLink); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			paths := testPaths(base)
			for _, directory := range []string{filepath.Dir(paths.NginxNativeUnit), filepath.Dir(paths.NginxWantsLink)} {
				if err := os.MkdirAll(directory, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(paths.NginxNativeUnit, []byte("[Unit]\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			createUnsafePath(t, paths)

			bundle := filepath.Join(base, "bundle")
			writeTestBundle(t, bundle)
			runner := &fakeRunner{}
			bootstrapper := &fakeBootstrapper{}
			identityLookups := 0
			identity := Identity{UID: os.Getuid(), GID: os.Getgid()}
			finalizer, err := New(Config{
				BundleRoot: bundle, ReleaseID: "v1.2.0", PlatformID: testPlatformID, Paths: paths,
				Runner: runner, Bootstrapper: bootstrapper,
				IdentityLookup: func(string) (Identity, error) {
					identityLookups++
					return identity, nil
				},
				RootIdentity: identity, RequireRoot: false,
				ValidateHostPlatform: acceptTestHostPlatform,
				ResolveHostname:      func(context.Context, string) error { return nil },
				CommitInstalled: func(time.Time) error {
					t.Fatal("unsafe Nginx enablement reached installed commit")
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			request := setup.CompleteRequest{
				Profile: setup.InstallationProfileManagement,
				Database: setup.DatabaseInput{
					Mode: "local", Name: "probe", Username: "probe",
					Password: setup.Secret("db-secret-1234"), PasswordConfirmation: setup.Secret("db-secret-1234"),
				},
				Domains: setup.DomainInput{}, Network: setup.NetworkInput{Address: "10.20.30.40"},
				TLS: setup.TLSInput{Mode: "private_ca"}, Allowlist: []string{"10.20.30.0/24"},
				Administrator: setup.AdministratorInput{
					Username: "admin", Password: setup.Secret("admin-secret-1234"), PasswordConfirmation: setup.Secret("admin-secret-1234"),
				},
			}
			err = finalizer.Finalize(context.Background(), request)
			if !errors.Is(err, ErrPreflight) || !strings.Contains(err.Error(), "Nginx enablement") {
				t.Fatalf("unsafe Nginx wants path was not rejected as preflight: %v", err)
			}
			if identityLookups != 0 || bootstrapper.called || len(runner.commands) != 0 || len(runner.sensitiveInputs) != 0 {
				t.Fatalf("unsafe Nginx wants path was detected after ownership: identities=%d bootstrap=%v commands=%v SQL=%d", identityLookups, bootstrapper.called, runner.commands, len(runner.sensitiveInputs))
			}
			if _, err := os.Lstat(paths.NginxWantsLink); err != nil {
				t.Fatalf("unsafe Nginx wants path was modified: %v", err)
			}
		})
	}
}

func TestRemoveNginxBootDependencyIsScopedAndIdempotent(t *testing.T) {
	t.Run("expected symlink and repeated missing relationship", func(t *testing.T) {
		paths := testPaths(t.TempDir())
		if err := os.MkdirAll(filepath.Dir(paths.NginxNativeUnit), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(paths.NginxNativeUnit, []byte("[Unit]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(paths.NginxWantsLink), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(paths.NginxNativeUnit, paths.NginxWantsLink); err != nil {
			t.Fatal(err)
		}
		runner := &fakeRunner{}
		finalizer := &Finalizer{config: Config{Paths: paths, Runner: runner}, platform: testPlatformContract(t, testPlatformID)}
		if err := finalizer.removeNginxBootDependency(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := finalizer.removeNginxBootDependency(context.Background()); err != nil {
			t.Fatalf("missing relationship was not idempotent: %v", err)
		}
		if _, err := os.Lstat(paths.NginxWantsLink); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("reviewed wants link was not removed: %v", err)
		}
		wantCommand := "/usr/bin/systemctl daemon-reload"
		if len(runner.commands) != 2 || runner.commands[0] != wantCommand || runner.commands[1] != wantCommand {
			t.Fatalf("systemd reload commands = %v", runner.commands)
		}
	})

	for name, createUnsafePath := range map[string]func(t *testing.T, paths Paths){
		"regular file": func(t *testing.T, paths Paths) {
			t.Helper()
			if err := os.WriteFile(paths.NginxWantsLink, []byte("not a symlink\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"unexpected symlink target": func(t *testing.T, paths Paths) {
			t.Helper()
			unexpectedUnit := filepath.Join(filepath.Dir(paths.NginxNativeUnit), "unexpected.service")
			if err := os.WriteFile(unexpectedUnit, []byte("[Unit]\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(unexpectedUnit, paths.NginxWantsLink); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			paths := testPaths(t.TempDir())
			if err := os.MkdirAll(filepath.Dir(paths.NginxNativeUnit), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.NginxNativeUnit, []byte("[Unit]\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(paths.NginxWantsLink), 0o755); err != nil {
				t.Fatal(err)
			}
			createUnsafePath(t, paths)
			runner := &fakeRunner{}
			finalizer := &Finalizer{config: Config{Paths: paths, Runner: runner}, platform: testPlatformContract(t, testPlatformID)}
			if err := finalizer.removeNginxBootDependency(context.Background()); err == nil {
				t.Fatal("unsafe nginx wants path was removed")
			}
			if _, err := os.Lstat(paths.NginxWantsLink); err != nil {
				t.Fatalf("unsafe nginx wants path was not preserved: %v", err)
			}
			if len(runner.commands) != 0 {
				t.Fatalf("unsafe path triggered systemd mutation: %v", runner.commands)
			}
		})
	}
}

func TestNginxNativeUnitCandidatesArePlatformScoped(t *testing.T) {
	base := t.TempDir()
	paths := testPaths(base)
	alternateUnit := filepath.Join(base, "lib", "systemd", "system", "nginx.service")
	for _, directory := range []string{filepath.Dir(alternateUnit), filepath.Dir(paths.NginxWantsLink)} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(alternateUnit, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(alternateUnit, paths.NginxWantsLink); err != nil {
		t.Fatal(err)
	}

	debianFinalizer := &Finalizer{
		config: Config{Paths: paths}, platform: testPlatformContract(t, PlatformDebian9Systemd),
	}
	if enabled, err := debianFinalizer.inspectNginxBootDependency(); err != nil || !enabled {
		t.Fatalf("Debian /lib nginx unit was rejected: enabled=%v error=%v", enabled, err)
	}

	rpmFinalizer := &Finalizer{
		config: Config{Paths: paths}, platform: testPlatformContract(t, PlatformCentOSLinux7Systemd),
	}
	if enabled, err := rpmFinalizer.inspectNginxBootDependency(); err == nil || enabled {
		t.Fatalf("RPM accepted the Debian-only /lib nginx unit: enabled=%v error=%v", enabled, err)
	}
}

func TestFreshIngressPreservesPreexistingNginxWantsRelationship(t *testing.T) {
	paths := testPaths(t.TempDir())
	for _, directory := range []string{paths.NginxConfD, paths.NginxSitesEnabled, filepath.Dir(paths.NginxWantsLink)} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(paths.NginxNativeUnit, paths.NginxWantsLink); err != nil {
		t.Fatal(err)
	}
	finalizer := &Finalizer{config: Config{Paths: paths}}
	err := finalizer.validateFreshIngress()
	if err == nil || !strings.Contains(err.Error(), paths.NginxWantsLink) {
		t.Fatalf("preexisting nginx wants relationship was accepted: %v", err)
	}
	if _, err := os.Lstat(paths.NginxWantsLink); err != nil {
		t.Fatalf("preexisting nginx wants relationship was modified: %v", err)
	}
}

func TestOnlyManagementIPAllowsForeignNginxConfiguration(t *testing.T) {
	paths := testPaths(t.TempDir())
	for _, directory := range []string{paths.NginxConfD, paths.NginxSitesEnabled} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(paths.NginxConfD, "existing-site.conf"), []byte("# existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	finalizer := &Finalizer{config: Config{Paths: paths}}
	managementIP := setup.AccessConfiguration{Mode: setup.IngressModeIP, Profile: setup.InstallationProfileManagement}
	if err := finalizer.validateFreshIngressForAccess(managementIP); err != nil {
		t.Fatalf("management IP rejected foreign Nginx configuration: %v", err)
	}
	managementDomain := setup.AccessConfiguration{Mode: setup.IngressModeDomain, Profile: setup.InstallationProfileManagement}
	if err := finalizer.validateFreshIngressForAccess(managementDomain); err == nil {
		t.Fatal("management domain incorrectly claimed shared-Nginx support")
	}
}

func TestManagementNginxReadOnlyPreflightRequiresIncludeAndFreeReservedPorts(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		mode    setup.IngressMode
		config  string
		wantErr string
	}{
		{name: "IP coexistence", mode: setup.IngressModeIP, config: "include %s/*.conf;\nlisten 8081;\n"},
		{name: "missing include", mode: setup.IngressModeIP, config: "listen 8081;\n", wantErr: "must include"},
		{name: "occupied IP port", mode: setup.IngressModeIP, config: "include %s/*.conf;\nlisten [::]:18455 ssl;\n", wantErr: "18455"},
		{name: "occupied domain HTTP", mode: setup.IngressModeDomain, config: "include %s/*.conf;\nlisten 80 default_server;\n", wantErr: "80"},
		{name: "occupied domain HTTPS", mode: setup.IngressModeDomain, config: "include %s/*.conf;\nlisten 127.0.0.1:443 ssl;\n", wantErr: "443"},
		{name: "unparseable listener", mode: setup.IngressModeIP, config: "include %s/*.conf;\nlisten inherited-port;\n", wantErr: "listen directive"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			paths := testPaths(t.TempDir())
			configuration := testCase.config
			if strings.Contains(configuration, "%s") {
				configuration = fmt.Sprintf(configuration, paths.NginxConfD)
			}
			runner := &fakeRunner{outputValues: map[string][]byte{
				"/usr/sbin/nginx -T": []byte(configuration),
			}}
			finalizer := &Finalizer{config: Config{Paths: paths, Runner: runner}}
			err := finalizer.validateManagementNginxPreflight(context.Background(), setup.AccessConfiguration{
				Mode: testCase.mode, Profile: setup.InstallationProfileManagement,
			})
			if testCase.wantErr == "" && err != nil {
				t.Fatalf("valid existing Nginx configuration rejected: %v", err)
			}
			if testCase.wantErr != "" && (err == nil || !strings.Contains(err.Error(), testCase.wantErr)) {
				t.Fatalf("configuration error=%v, want %q", err, testCase.wantErr)
			}
		})
	}
}

func TestManagementFreshPreflightRejectsEveryLateDeploymentCollision(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mode   setup.IngressMode
		target func(Paths) string
	}{
		{
			name: "domain deploy hook", mode: setup.IngressModeDomain,
			target: func(paths Paths) string { return paths.LetsEncryptDeployHook },
		},
		{
			name: "domain certificate archive", mode: setup.IngressModeDomain,
			target: func(paths Paths) string { return paths.LetsEncryptArchive },
		},
		{
			name: "domain renewal configuration", mode: setup.IngressModeDomain,
			target: func(paths Paths) string { return paths.LetsEncryptRenewal },
		},
		{
			name: "domain administrator certificate link", mode: setup.IngressModeDomain,
			target: func(paths Paths) string { return filepath.Join(paths.TLSRoot, "admin", "fullchain.pem") },
		},
		{
			name: "IP administrator key link", mode: setup.IngressModeIP,
			target: func(paths Paths) string { return filepath.Join(paths.TLSRoot, "admin", "privkey.pem") },
		},
		{
			name: "API environment example", mode: setup.IngressModeIP,
			target: func(paths Paths) string { return filepath.Join(paths.ConfigDir, "probe-api.env.example") },
		},
		{
			name: "backup environment example", mode: setup.IngressModeIP,
			target: func(paths Paths) string { return filepath.Join(paths.ConfigDir, "probe-postgres-backup.env.example") },
		},
		{
			name: "management domain Nginx example", mode: setup.IngressModeIP,
			target: func(paths Paths) string { return filepath.Join(paths.NginxConfigDir, "nginx-management.conf.example") },
		},
		{
			name: "management IP Nginx example", mode: setup.IngressModeIP,
			target: func(paths Paths) string {
				return filepath.Join(paths.NginxConfigDir, "nginx-management-ip.conf.example")
			},
		},
		{
			name: "PostgreSQL backup script", mode: setup.IngressModeIP,
			target: func(paths Paths) string { return filepath.Join(paths.BackupScriptDir, "backup-postgres.sh") },
		},
		{
			name: "PostgreSQL restore script", mode: setup.IngressModeIP,
			target: func(paths Paths) string { return filepath.Join(paths.BackupScriptDir, "restore-postgres.sh") },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			paths := testPaths(t.TempDir())
			target := testCase.target(paths)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("foreign\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			finalizer := &Finalizer{config: Config{Paths: paths}}
			access := setup.AccessConfiguration{Mode: testCase.mode, Profile: setup.InstallationProfileManagement}
			err := finalizer.validateFreshIngressForAccess(access)
			if err == nil || !strings.Contains(err.Error(), target) {
				t.Fatalf("late deployment collision %s was not rejected by fresh preflight: %v", target, err)
			}
		})
	}
}

func TestManagedDirectoryValidationNeverRepairsExistingPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	identity := Identity{UID: os.Getuid(), GID: os.Getgid()}
	err := ensureDirectory(directorySpec{path: path, mode: 0o700, identity: identity})
	if err == nil {
		t.Fatal("existing managed directory with incompatible permissions was repaired")
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("existing managed directory mode changed to %o", info.Mode().Perm())
	}
}

func TestManagedDirectoryCreationAppliesExactModeUnderRestrictiveUmask(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed")
	previousUmask := syscall.Umask(0o077)
	defer syscall.Umask(previousUmask)

	identity := Identity{UID: os.Getuid(), GID: os.Getgid()}
	if err := ensureDirectory(directorySpec{path: path, mode: 0o755, identity: identity}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("new managed directory mode = %o; want 755", info.Mode().Perm())
	}
}

func TestSharedDirectoryPreservesExistingSecurePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	identity := Identity{UID: os.Getuid(), GID: os.Getgid()}
	if err := ensureSharedRootDirectory(directorySpec{path: path, mode: 0o755, identity: identity}); err != nil {
		t.Fatalf("secure shared directory was rejected: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("shared directory mode changed to %o", info.Mode().Perm())
	}
}

func TestLayoutPreflightRejectsIncompatibleExistingDirectoryWithoutMutation(t *testing.T) {
	paths := testPaths(t.TempDir())
	mkdirAllWithMode(t, paths.ProbeRoot, 0o755)
	mkdirAllWithMode(t, paths.ConfigDir, 0o755)
	identity := Identity{UID: os.Getuid(), GID: os.Getgid()}
	finalizer := &Finalizer{config: Config{Paths: paths, RootIdentity: identity}}
	err := finalizer.validateLayoutPreflight(identity, setup.AccessConfiguration{
		Mode: setup.IngressModeIP, Profile: setup.InstallationProfileManagement,
	})
	if err == nil || !strings.Contains(err.Error(), paths.ConfigDir) {
		t.Fatalf("incompatible existing layout was accepted: %v", err)
	}
	info, statErr := os.Stat(paths.ConfigDir)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("preflight mutated existing layout mode to %o", info.Mode().Perm())
	}
}

func TestDomainLayoutPreflightRejectsSymlinkedCertbotNamespace(t *testing.T) {
	paths := testPaths(t.TempDir())
	liveParent := filepath.Dir(paths.LetsEncryptLive)
	if err := os.MkdirAll(filepath.Dir(liveParent), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := t.TempDir()
	if err := os.Symlink(foreign, liveParent); err != nil {
		t.Fatal(err)
	}
	identity := Identity{UID: os.Getuid(), GID: os.Getgid()}
	finalizer := &Finalizer{config: Config{Paths: paths, RootIdentity: identity}}
	err := finalizer.validateLayoutPreflight(identity, setup.AccessConfiguration{
		Mode: setup.IngressModeDomain, Profile: setup.InstallationProfileManagement,
	})
	if err == nil || !strings.Contains(err.Error(), liveParent) {
		t.Fatalf("symlinked Certbot namespace was accepted: %v", err)
	}
	info, statErr := os.Lstat(liveParent)
	if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("preflight altered Certbot symlink: info=%v error=%v", info, statErr)
	}
}

func TestIPIngressPreflightRejectsEveryReservedPort(t *testing.T) {
	for _, port := range []string{"18453", "18454", "18455"} {
		t.Run(port, func(t *testing.T) {
			runner := &fakeRunner{listeners: []byte("LISTEN 0 128 0.0.0.0:" + port + " 0.0.0.0:*\n")}
			finalizer := &Finalizer{config: Config{Runner: runner}, platform: testPlatformContract(t, testPlatformID)}
			if err := finalizer.validateAvailableIPPorts(context.Background()); err == nil || !strings.Contains(err.Error(), port) {
				t.Fatalf("occupied port %s was accepted: %v", port, err)
			}
		})
	}
}

func TestManagementIPIngressOwnsOnlyAdministratorPort(t *testing.T) {
	for _, port := range []string{"18453", "18454"} {
		runner := &fakeRunner{listeners: []byte("LISTEN 0 128 0.0.0.0:" + port + " 0.0.0.0:*\n")}
		finalizer := &Finalizer{config: Config{Runner: runner}, platform: testPlatformContract(t, testPlatformID)}
		if err := finalizer.validateAvailableIPPortsForProfile(context.Background(), setup.InstallationProfileManagement); err != nil {
			t.Fatalf("management profile rejected independent port %s: %v", port, err)
		}
	}
	runner := &fakeRunner{listeners: []byte("LISTEN 0 128 0.0.0.0:18455 0.0.0.0:*\n")}
	finalizer := &Finalizer{config: Config{Runner: runner}, platform: testPlatformContract(t, testPlatformID)}
	if err := finalizer.validateAvailableIPPortsForProfile(context.Background(), setup.InstallationProfileManagement); err == nil || !strings.Contains(err.Error(), "18455") {
		t.Fatalf("management profile accepted occupied administrator port: %v", err)
	}
}

func TestManagementCertificateTimerConfigurationPreservesExistingEnablement(t *testing.T) {
	access := setup.AccessConfiguration{Mode: setup.IngressModeDomain, Profile: setup.InstallationProfileManagement}
	for unitFileState, expectedCommands := range map[string][]string{
		"disabled": []string{
			"/usr/bin/systemctl enable certbot.timer",
			"/usr/bin/systemctl start certbot.timer",
		},
		"enabled":         []string{"/usr/bin/systemctl start certbot.timer"},
		"enabled-runtime": []string{"/usr/bin/systemctl start certbot.timer"},
	} {
		t.Run(unitFileState, func(t *testing.T) {
			runner := &fakeRunner{}
			finalizer := &Finalizer{
				config:                         Config{Runner: runner},
				platform:                       testPlatformContract(t, testPlatformID),
				managementCertbotStateCaptured: true,
				managementCertbotUnitFileState: unitFileState,
			}
			if err := finalizer.configureCertificateTimer(context.Background(), access); err != nil {
				t.Fatal(err)
			}
			if got, want := strings.Join(runner.commands, "\n"), strings.Join(expectedCommands, "\n"); got != want {
				t.Fatalf("certificate timer configuration = %q; want %q", got, want)
			}
			if strings.Contains(strings.Join(runner.commands, "\n"), "--now") {
				t.Fatal("runtime certificate timer enablement was made persistent")
			}
		})
	}
}

func TestCaptureManagementCertificateTimerStateIsExact(t *testing.T) {
	for _, unitFileState := range []string{"disabled", "enabled", "enabled-runtime"} {
		for _, activeState := range []string{"inactive", "active"} {
			t.Run(unitFileState+"/"+activeState, func(t *testing.T) {
				runner := &fakeRunner{outputValues: map[string][]byte{
					"/usr/bin/systemctl show --property=UnitFileState certbot.timer": []byte("UnitFileState=" + unitFileState + "\n"),
					"/usr/bin/systemctl show --property=ActiveState certbot.timer":   []byte("ActiveState=" + activeState + "\n"),
				}}
				finalizer := &Finalizer{config: Config{Runner: runner}, platform: testPlatformContract(t, testPlatformID)}
				if err := finalizer.captureManagementCertbotState(context.Background()); err != nil {
					t.Fatal(err)
				}
				if !finalizer.managementCertbotStateCaptured || finalizer.managementCertbotUnitFileState != unitFileState || finalizer.managementCertbotWasActive != (activeState == "active") {
					t.Fatalf("captured certificate timer state = captured:%v unit:%q active:%v", finalizer.managementCertbotStateCaptured, finalizer.managementCertbotUnitFileState, finalizer.managementCertbotWasActive)
				}
			})
		}
	}
}

func TestSystemdPropertyParsesOldSystemdPropertyRecordsStrictly(t *testing.T) {
	command := "/usr/bin/systemctl show --property=ActiveState nginx.service"
	runner := &fakeRunner{outputValues: map[string][]byte{
		command: []byte("ActiveState=active\n"),
	}}
	finalizer := &Finalizer{config: Config{Runner: runner}}
	value, err := finalizer.systemdProperty(context.Background(), "nginx.service", "ActiveState")
	if err != nil || value != "active" {
		t.Fatalf("valid property record = %q, %v", value, err)
	}
	if len(runner.commands) != 1 || runner.commands[0] != command || strings.Contains(runner.commands[0], "--value") {
		t.Fatalf("systemd property command = %v", runner.commands)
	}

	for name, output := range map[string][]byte{
		"bare value":       []byte("active\n"),
		"wrong property":   []byte("SubState=active\n"),
		"empty value":      []byte("ActiveState=\n"),
		"leading space":    []byte("ActiveState= active\n"),
		"trailing space":   []byte("ActiveState=active \n"),
		"multiple records": []byte("ActiveState=active\nSubState=running\n"),
		"extra newline":    []byte("ActiveState=active\n\n"),
	} {
		t.Run(name, func(t *testing.T) {
			runner := &fakeRunner{outputValues: map[string][]byte{command: output}}
			finalizer := &Finalizer{config: Config{Runner: runner}}
			if value, err := finalizer.systemdProperty(context.Background(), "nginx.service", "ActiveState"); err == nil || value != "" {
				t.Fatalf("invalid property output accepted: value=%q error=%v", value, err)
			}
		})
	}
}

func TestRPMCertificateTimerUsesCompiledUnitName(t *testing.T) {
	runner := &fakeRunner{}
	finalizer := &Finalizer{
		config: Config{Runner: runner}, platform: testPlatformContract(t, PlatformCentOSLinux7Systemd),
		managementCertbotStateCaptured: true, managementCertbotUnitFileState: "disabled",
	}
	access := setup.AccessConfiguration{Mode: setup.IngressModeDomain, Profile: setup.InstallationProfileManagement}
	if err := finalizer.configureCertificateTimer(context.Background(), access); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"/usr/bin/systemctl enable certbot-renew.timer",
		"/usr/bin/systemctl start certbot-renew.timer",
	}, "\n")
	if got := strings.Join(runner.commands, "\n"); got != want || strings.Contains(got, "--now") {
		t.Fatalf("RPM certificate timer commands = %q; want %q", got, want)
	}
}

func TestManagementCertificateTimerRollbackRestoresExactIndependentStates(t *testing.T) {
	for _, unitFileState := range []string{"disabled", "enabled", "enabled-runtime"} {
		for _, wasActive := range []bool{false, true} {
			name := unitFileState + "/inactive"
			activityCommand := "/usr/bin/systemctl stop certbot.timer"
			if wasActive {
				name = unitFileState + "/active"
				activityCommand = "/usr/bin/systemctl start certbot.timer"
			}
			t.Run(name, func(t *testing.T) {
				runner := &fakeRunner{}
				finalizer := &Finalizer{
					config:                         Config{Runner: runner},
					platform:                       testPlatformContract(t, testPlatformID),
					managementCertbotStateCaptured: true,
					managementCertbotUnitFileState: unitFileState,
					managementCertbotWasActive:     wasActive,
				}
				if err := finalizer.restoreManagementCertbotState(context.Background()); err != nil {
					t.Fatal(err)
				}
				expected := []string{}
				switch unitFileState {
				case "disabled":
					expected = append(expected, "/usr/bin/systemctl disable certbot.timer")
				case "enabled":
					expected = append(expected, "/usr/bin/systemctl enable certbot.timer")
				case "enabled-runtime":
					expected = append(expected,
						"/usr/bin/systemctl disable certbot.timer",
						"/usr/bin/systemctl enable --runtime certbot.timer",
					)
				}
				expected = append(expected, activityCommand)
				if got, want := strings.Join(runner.commands, "\n"), strings.Join(expected, "\n"); got != want {
					t.Fatalf("certificate timer rollback = %q; want %q", got, want)
				}
			})
		}
	}
}

func TestManagementRollbackPreservesSharedNginxAndCertbot(t *testing.T) {
	base := t.TempDir()
	paths := testPaths(base)
	for _, directory := range []string{filepath.Dir(paths.ActiveNginxConfig), paths.NginxConfD, filepath.Dir(paths.NginxWantsLink), filepath.Dir(paths.NginxNativeUnit)} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(paths.ActiveNginxConfig, []byte("# generated management config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(paths.ActiveNginxConfig, paths.NginxLink); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.NginxNativeUnit, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(paths.NginxNativeUnit, paths.NginxWantsLink); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	finalizer := &Finalizer{
		config:                         Config{Paths: paths, Runner: runner},
		platform:                       testPlatformContract(t, testPlatformID),
		managementNginxStateCaptured:   true,
		managementNginxWasActive:       true,
		managementCertbotStateCaptured: true,
		managementCertbotUnitFileState: "enabled",
		managementCertbotWasActive:     true,
	}
	if err := finalizer.stopProductionForProfile(setup.InstallationProfileManagement, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(paths.NginxLink); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("management Nginx link was not removed: %v", err)
	}
	if _, err := os.Lstat(paths.NginxWantsLink); err != nil {
		t.Fatalf("preexisting Nginx enablement was not preserved: %v", err)
	}
	commands := strings.Join(runner.commands, "\n")
	for _, required := range []string{
		"/usr/bin/systemctl enable certbot.timer",
		"/usr/bin/systemctl start certbot.timer",
		"/usr/sbin/nginx -t",
		"/usr/bin/systemctl reload nginx.service",
	} {
		if !strings.Contains(commands, required) {
			t.Fatalf("management rollback is missing %q: %s", required, commands)
		}
	}
	for _, forbidden := range []string{"stop nginx.service", "disable nginx.service"} {
		if strings.Contains(commands, forbidden) {
			t.Fatalf("management rollback touched shared service %q: %s", forbidden, commands)
		}
	}
}

func TestManagementRollbackRestoresPreviouslyInactiveSharedServices(t *testing.T) {
	paths := testPaths(t.TempDir())
	for _, directory := range []string{filepath.Dir(paths.ActiveNginxConfig), paths.NginxConfD} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(paths.ActiveNginxConfig, []byte("# generated management config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(paths.ActiveNginxConfig, paths.NginxLink); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	finalizer := &Finalizer{
		config:                         Config{Paths: paths, Runner: runner},
		platform:                       testPlatformContract(t, testPlatformID),
		managementNginxStateCaptured:   true,
		managementNginxWasActive:       false,
		managementCertbotStateCaptured: true,
		managementCertbotUnitFileState: "disabled",
		managementCertbotWasActive:     false,
	}
	if err := finalizer.stopProductionForProfile(setup.InstallationProfileManagement, true); err != nil {
		t.Fatal(err)
	}
	commands := strings.Join(runner.commands, "\n")
	for _, required := range []string{
		"/usr/bin/systemctl stop certbot.timer",
		"/usr/bin/systemctl disable certbot.timer",
		"/usr/bin/systemctl stop nginx.service",
	} {
		if !strings.Contains(commands, required) {
			t.Fatalf("management rollback is missing %q: %s", required, commands)
		}
	}
	if strings.Contains(commands, "/usr/bin/systemctl reload nginx.service") {
		t.Fatalf("rollback started a previously inactive shared Nginx service: %s", commands)
	}
}

func TestIPIngressRejectsOccupiedPortBeforePersistentMutation(t *testing.T) {
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
	runner := &fakeRunner{listeners: []byte("LISTEN 0 128 0.0.0.0:18454 0.0.0.0:*\n")}
	bootstrapper := &fakeBootstrapper{}
	identityLookups := 0
	currentIdentity := Identity{UID: os.Getuid(), GID: os.Getgid()}
	finalizer, err := New(Config{
		BundleRoot: bundle, ReleaseID: "v1.0.0", PlatformID: testPlatformID, Paths: paths,
		Runner: runner, Bootstrapper: bootstrapper,
		IdentityLookup: func(string) (Identity, error) {
			identityLookups++
			return currentIdentity, nil
		},
		RootIdentity: currentIdentity, RequireRoot: false,
		ValidateHostPlatform: acceptTestHostPlatform,
		ResolveHostname: func(context.Context, string) error {
			t.Fatal("IP preflight attempted DNS resolution")
			return nil
		},
		CommitInstalled: func(time.Time) error {
			t.Fatal("occupied IP port reached installed commit")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := setup.CompleteRequest{
		Database: setup.DatabaseInput{
			Mode: "local", Name: "probe", Username: "probe",
			Password: setup.Secret("db-secret-1234"), PasswordConfirmation: setup.Secret("db-secret-1234"),
		},
		Domains: setup.DomainInput{}, Network: setup.NetworkInput{Address: "10.20.30.40"},
		TLS: setup.TLSInput{Mode: "private_ca"}, Allowlist: []string{"10.20.30.0/24"},
		Administrator: setup.AdministratorInput{
			Username: "admin", Password: setup.Secret("admin-secret-1234"), PasswordConfirmation: setup.Secret("admin-secret-1234"),
		},
	}
	err = finalizer.Finalize(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "18454") {
		t.Fatalf("occupied IP port was accepted: %v", err)
	}
	if identityLookups != 0 || bootstrapper.called || len(runner.sensitiveInputs) != 0 {
		t.Fatalf("IP port preflight ran too late: identities=%d bootstrap=%v SQL=%d", identityLookups, bootstrapper.called, len(runner.sensitiveInputs))
	}
	if _, statErr := os.Lstat(paths.APIEnvironment); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("occupied IP port allowed configuration persistence: %v", statErr)
	}
}

func TestIngressPortInspectionFailsClosedOnCommandOrParseFailure(t *testing.T) {
	tests := map[string]*fakeRunner{
		"command failure": {listenerError: errors.New("ss unavailable")},
		"short record":    {listeners: []byte("LISTEN broken\n")},
		"wrong state":     {listeners: []byte("ESTAB 0 0 127.0.0.1:18453 127.0.0.1:1\n")},
		"invalid port":    {listeners: []byte("LISTEN 0 128 127.0.0.1:not-a-port 0.0.0.0:*\n")},
		"invalid address": {listeners: []byte("LISTEN 0 128 not-an-address:18453 0.0.0.0:*\n")},
	}
	for name, runner := range tests {
		t.Run(name, func(t *testing.T) {
			finalizer := &Finalizer{config: Config{Runner: runner}, platform: testPlatformContract(t, testPlatformID)}
			if err := finalizer.validateAvailableIPPorts(context.Background()); err == nil {
				t.Fatal("unreliable ss result was accepted")
			}
		})
	}
	finalizer := &Finalizer{config: Config{Runner: &fakeRunner{listeners: []byte{}}}, platform: testPlatformContract(t, testPlatformID)}
	if err := finalizer.validateAvailableIPPorts(context.Background()); err != nil {
		t.Fatalf("valid empty listener set was rejected: %v", err)
	}
}

func TestDomainIngressRejectsOccupiedACMEPortsBeforePersistentMutation(t *testing.T) {
	for _, port := range []string{"80", "443"} {
		t.Run(port, func(t *testing.T) {
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
			runner := &fakeRunner{listeners: []byte("LISTEN 0 128 0.0.0.0:" + port + " 0.0.0.0:*\n")}
			bootstrapper := &fakeBootstrapper{}
			identityLookups := 0
			resolveCalls := 0
			currentIdentity := Identity{UID: os.Getuid(), GID: os.Getgid()}
			finalizer, err := New(Config{
				BundleRoot: bundle, ReleaseID: "v1.0.0", PlatformID: testPlatformID, Paths: paths,
				Runner: runner, Bootstrapper: bootstrapper,
				IdentityLookup: func(string) (Identity, error) {
					identityLookups++
					return currentIdentity, nil
				},
				RootIdentity: currentIdentity, RequireRoot: false,
				ValidateHostPlatform: acceptTestHostPlatform,
				ResolveHostname: func(context.Context, string) error {
					resolveCalls++
					return nil
				},
				CommitInstalled: func(time.Time) error {
					t.Fatal("occupied ACME port reached installed commit")
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			request := setup.CompleteRequest{
				Database: setup.DatabaseInput{
					Mode: "local", Name: "probe", Username: "probe",
					Password: setup.Secret("db-secret-1234"), PasswordConfirmation: setup.Secret("db-secret-1234"),
				},
				Domains: setup.DomainInput{Panel: "panel.monitor.test", Admin: "admin.monitor.test", Agent: "api.monitor.test"},
				Network: setup.NetworkInput{}, TLS: setup.TLSInput{Mode: "acme", Email: "admin@monitor.test"},
				Allowlist: []string{"203.0.113.25"},
				Administrator: setup.AdministratorInput{
					Username: "admin", Password: setup.Secret("admin-secret-1234"), PasswordConfirmation: setup.Secret("admin-secret-1234"),
				},
			}
			err = finalizer.Finalize(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), port) {
				t.Fatalf("occupied ACME port %s was accepted: %v", port, err)
			}
			if identityLookups != 0 || resolveCalls != 0 || bootstrapper.called || len(runner.sensitiveInputs) != 0 {
				t.Fatalf("port preflight ran too late: identities=%d DNS=%d bootstrap=%v SQL=%d", identityLookups, resolveCalls, bootstrapper.called, len(runner.sensitiveInputs))
			}
			if _, statErr := os.Lstat(paths.APIEnvironment); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("occupied ACME port allowed configuration persistence: %v", statErr)
			}
		})
	}
}

func TestIPNginxTemplateHasIsolatedCookieAndCAContracts(t *testing.T) {
	template, err := os.ReadFile(filepath.Join("..", "..", "deploy", "nginx", "nginx-ip.conf"))
	if err != nil {
		t.Fatal(err)
	}
	contents := string(template)
	for _, required := range []string{
		"PROBE_SETUP_SERVER_IP:18453", "PROBE_SETUP_SERVER_IP:18454", "PROBE_SETUP_SERVER_IP:18455",
		"proxy_set_header Cookie \"\";", "proxy_hide_header Set-Cookie;",
		"location = /install", "location ^~ /install/",
		"location = /downloads/probe-agent/ca.pem",
		"alias /etc/probe-panel/tls/private-ca/ca.pem;",
	} {
		if !strings.Contains(contents, required) {
			t.Fatalf("IP Nginx template is missing %q", required)
		}
	}
	if strings.Count(contents, "proxy_set_header Cookie \"\";") != 2 || strings.Count(contents, "proxy_hide_header Set-Cookie;") != 2 {
		t.Fatal("visitor and Agent surfaces must each strip cookies in both directions")
	}
	if strings.Contains(contents, "listen 80") || strings.Contains(contents, "listen 443") || strings.Contains(contents, "Strict-Transport-Security") {
		t.Fatal("private-CA template opened ACME ports or enabled HSTS")
	}
	firstSPA := strings.Index(contents, "root /srv/probe/web;")
	exactReject := strings.Index(contents, "location = /install")
	prefixReject := strings.Index(contents, "location ^~ /install/")
	if firstSPA < 0 || exactReject < 0 || prefixReject < 0 || exactReject > firstSPA || prefixReject > firstSPA {
		t.Fatal("IP visitor template allows an install route to enter the SPA fallback")
	}
	request := setup.CompleteRequest{
		Domains: setup.DomainInput{}, Network: setup.NetworkInput{Address: "2001:db8::25"},
		TLS: setup.TLSInput{Mode: "private_ca"},
	}
	access, err := request.AccessConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := renderIPNginx(template, access)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rendered), "PROBE_SETUP_SERVER_IP") || !strings.Contains(string(rendered), "[2001:db8::25]:18455") {
		t.Fatal("IPv6 Nginx rendering did not replace every stable placeholder")
	}
}

func TestManagementNginxTemplatesExposeManagementAndCredentialedAgentAPI(t *testing.T) {
	domainTemplate, err := os.ReadFile(filepath.Join("..", "..", "deploy", "nginx", "nginx-management.conf"))
	if err != nil {
		t.Fatal(err)
	}
	domainContents := string(domainTemplate)
	for _, required := range []string{
		"admin.example.com", "location ~ ^/api/v1/(?:auth|admin)",
		"location ~ ^/api/v1/panel", "location = /api/v1/agent/enroll",
		"location = /api/v1/agent/config", "location = /api/v1/agent/report",
		"root /srv/probe/admin;",
	} {
		if !strings.Contains(domainContents, required) {
			t.Fatalf("management domain template is missing %q", required)
		}
	}
	for _, forbidden := range []string{"panel.example.com", "api.example.com", "/downloads/probe-agent", "/srv/probe/web", "/srv/probe/agent"} {
		if strings.Contains(domainContents, forbidden) {
			t.Fatalf("management domain template contains independent surface %q", forbidden)
		}
	}
	if strings.Count(domainContents, "proxy_set_header Cookie \"\";") != 3 ||
		strings.Count(domainContents, "proxy_hide_header Set-Cookie;") != 3 {
		t.Fatal("every management-host Agent API route must strip browser cookies")
	}
	renderedDomain, err := renderManagementNginx(domainTemplate, "admin.monitor.test")
	if err != nil || strings.Contains(string(renderedDomain), "admin.example.com") || !strings.Contains(string(renderedDomain), "admin.monitor.test") {
		t.Fatalf("management domain rendering failed: %v", err)
	}

	ipTemplate, err := os.ReadFile(filepath.Join("..", "..", "deploy", "nginx", "nginx-management-ip.conf"))
	if err != nil {
		t.Fatal(err)
	}
	ipContents := string(ipTemplate)
	if !strings.Contains(ipContents, "PROBE_SETUP_SERVER_IP:18455") || strings.Contains(ipContents, "18453") || strings.Contains(ipContents, "18454") || strings.Contains(ipContents, "listen 80") || strings.Contains(ipContents, "listen 443") {
		t.Fatalf("management IP listener contract is invalid")
	}
	for _, required := range []string{"location = /api/v1/agent/enroll", "location = /api/v1/agent/config", "location = /api/v1/agent/report"} {
		if !strings.Contains(ipContents, required) {
			t.Fatalf("management IP template is missing %q", required)
		}
	}
	request := setup.CompleteRequest{
		Profile: setup.InstallationProfileManagement,
		Domains: setup.DomainInput{}, Network: setup.NetworkInput{Address: "2001:db8::25"},
		TLS: setup.TLSInput{Mode: "private_ca"},
	}
	access, err := request.AccessConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	renderedIP, err := renderIPNginx(ipTemplate, access)
	if err != nil || strings.Contains(string(renderedIP), "PROBE_SETUP_SERVER_IP") || !strings.Contains(string(renderedIP), "[2001:db8::25]:18455") {
		t.Fatalf("management IPv6 rendering failed: %v", err)
	}
}

func TestManagementFinalizerUnitHasNarrowBindAllowlist(t *testing.T) {
	unit, err := os.ReadFile(filepath.Join("..", "..", "deploy", "setup", "probe-panel-finalizer-management.service"))
	if err != nil {
		t.Fatal(err)
	}
	contents := string(unit)
	for _, required := range []string{"SocketBindAllow=tcp:80", "SocketBindAllow=tcp:443", "SocketBindAllow=tcp:18455", "SocketBindDeny=any"} {
		if !strings.Contains(contents, required) {
			t.Fatalf("management finalizer unit is missing %q", required)
		}
	}
	if strings.Contains(contents, "SocketBindAllow=tcp:18453") || strings.Contains(contents, "SocketBindAllow=tcp:18454") || strings.Count(contents, "SocketBindAllow=") != 3 {
		t.Fatal("management finalizer unit can bind an independent surface")
	}
	if strings.Count(contents, "ExecStopPost=") != 1 ||
		!strings.Contains(contents, "ExecStopPost=/usr/local/lib/probe-panel/probe-setup finalize-cleanup") ||
		strings.Contains(contents, "ExecStopPost=/usr/bin/systemctl") ||
		strings.Contains(contents, "ExecStopPost=/usr/bin/sleep") {
		t.Fatal("management finalizer must delegate retry-aware setup cleanup to probe-setup")
	}
}

func TestDomainNginxVisitorSurfacePermanentlyRejectsInstallRoutes(t *testing.T) {
	template, err := os.ReadFile(filepath.Join("..", "..", "deploy", "nginx", "nginx.conf"))
	if err != nil {
		t.Fatal(err)
	}
	contents := string(template)
	firstSPA := strings.Index(contents, "root /srv/probe/web;")
	exactReject := strings.Index(contents, "location = /install")
	prefixReject := strings.Index(contents, "location ^~ /install/")
	if firstSPA < 0 || exactReject < 0 || prefixReject < 0 || exactReject > firstSPA || prefixReject > firstSPA {
		t.Fatal("domain visitor template allows an install route to enter the SPA fallback")
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

func TestPostgresCommandUsesCleanEnvironmentAndPlatformCapabilities(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		platformID  string
		psqlPath    string
		wantAmbient bool
	}{
		{name: "Debian", platformID: PlatformDebian9Systemd, psqlPath: debianPsqlPath, wantAmbient: true},
		{name: "CentOS 7", platformID: PlatformCentOSLinux7Systemd, psqlPath: rpmPsqlPath, wantAmbient: false},
		{name: "CentOS Stream 9", platformID: PlatformCentOSStream9Systemd, psqlPath: rpmPsqlPath, wantAmbient: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			finalizer := &Finalizer{platform: testPlatformContract(t, testCase.platformID)}
			arguments := finalizer.postgresCommand("--version")
			wantEnvironment := []string{
				"-i",
				"HOME=/var/lib/postgresql",
				"USER=postgres",
				"LOGNAME=postgres",
				"SHELL=/bin/sh",
				"PATH=" + postgresCommandPath,
			}
			if len(arguments) <= len(wantEnvironment) || strings.Join(arguments[:len(wantEnvironment)], "\n") != strings.Join(wantEnvironment, "\n") || arguments[len(wantEnvironment)] != "/usr/bin/setpriv" {
				t.Fatalf("PostgreSQL environment prefix = %q", arguments)
			}
			command := "/usr/bin/env " + strings.Join(arguments, " ")
			if strings.Count(command, "/usr/bin/setpriv") != 2 || strings.Contains(command, "--reset-env") ||
				strings.Contains(command, "HOME=/root") || strings.Contains(command, "USER=root") || strings.Contains(command, "LOGNAME=root") {
				t.Fatalf("PostgreSQL privilege boundary inherited root state: %s", command)
			}
			if !strings.Contains(command, "--inh-caps=-all") || strings.Contains(command, "--ambient-caps=-all") != testCase.wantAmbient {
				t.Fatalf("PostgreSQL capability clearing contract = %s", command)
			}
			if !strings.Contains(command, " -- "+testCase.psqlPath+" --version") {
				t.Fatalf("PostgreSQL command did not select %s: %s", testCase.psqlPath, command)
			}
		})
	}
}

func TestPostgreSQLServerVersionGateRequiresVersion14(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		output  string
		wantErr bool
	}{
		{name: "minimum", output: "140000\n"},
		{name: "latest 14", output: "149999\n"},
		{name: "newer major", output: "150000\n", wantErr: true},
		{name: "much newer major", output: "170004\n", wantErr: true},
		{name: "too old", output: "139999\n", wantErr: true},
		{name: "not numeric", output: "PostgreSQL 14\n", wantErr: true},
		{name: "multiple records", output: "140000\n140001\n", wantErr: true},
		{name: "empty", output: "\n", wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runner := &fakeRunner{postgresVersion: []byte(testCase.output)}
			finalizer := &Finalizer{
				config: Config{Runner: runner}, platform: testPlatformContract(t, PlatformDebian9Systemd),
			}
			err := finalizer.validatePostgresServerVersion(context.Background())
			if (err != nil) != testCase.wantErr {
				t.Fatalf("version output %q error = %v", testCase.output, err)
			}
			commands := strings.Join(runner.commands, "\n")
			if !strings.Contains(commands, "/usr/bin/env -i ") || !strings.Contains(commands, debianPsqlPath+" --no-psqlrc") || !strings.Contains(commands, "SHOW server_version_num;") {
				t.Fatalf("version gate command = %s", commands)
			}
		})
	}
}

func TestRPMPostgreSQLRuntimeUsesPGDG14Paths(t *testing.T) {
	runner := &fakeRunner{listeners: []byte("LISTEN 0 128 192.168.33.253:5432 0.0.0.0:*\n")}
	finalizer := &Finalizer{
		config: Config{Runner: runner}, platform: testPlatformContract(t, PlatformCentOSLinux7Systemd),
	}
	err := finalizer.startLocalPostgres(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not restricted") {
		t.Fatalf("RPM PostgreSQL listener error = %v", err)
	}
	commands := strings.Join(runner.commands, "\n")
	for _, required := range []string{
		"/usr/bin/systemctl start postgresql-14.service",
		"/usr/bin/systemctl is-active --quiet postgresql-14.service",
		"/usr/pgsql-14/bin/pg_isready --host 127.0.0.1",
		"/usr/sbin/ss -H -lnt",
	} {
		if !strings.Contains(commands, required) {
			t.Fatalf("RPM PostgreSQL runtime is missing %q: %s", required, commands)
		}
	}
}

func TestPostgreSQLListenerRestrictionAcceptsOnlyExactLoopback(t *testing.T) {
	valid := map[string][]byte{
		"IPv4": []byte("LISTEN 0 128 127.0.0.1:5432 0.0.0.0:*\n"),
		"IPv6": []byte("LISTEN 0 128 [::1]:5432 [::]:*\n"),
		"dual stack": []byte(strings.Join([]string{
			"LISTEN 0 128 127.0.0.1:5432 0.0.0.0:*",
			"LISTEN 0 128 [::1]:5432 [::]:*", "",
		}, "\n")),
		"unrelated external port": []byte(strings.Join([]string{
			"LISTEN 0 128 127.0.0.1:5432 0.0.0.0:*",
			"LISTEN 0 128 192.0.2.20:8080 0.0.0.0:*", "",
		}, "\n")),
	}
	for name, output := range valid {
		t.Run(name, func(t *testing.T) {
			allowed, err := tcpPortRestrictedToLoopback(output, "5432")
			if err != nil || !allowed {
				t.Fatalf("loopback listener rejected: allowed=%v error=%v", allowed, err)
			}
		})
	}

	for name, output := range map[string][]byte{
		"wildcard IPv4":              []byte("LISTEN 0 128 0.0.0.0:5432 0.0.0.0:*\n"),
		"wildcard IPv6":              []byte("LISTEN 0 128 [::]:5432 [::]:*\n"),
		"private address":            []byte("LISTEN 0 128 192.168.33.253:5432 0.0.0.0:*\n"),
		"other IPv4 loopback":        []byte("LISTEN 0 128 127.0.0.2:5432 0.0.0.0:*\n"),
		"no PostgreSQL TCP listener": []byte("LISTEN 0 128 127.0.0.1:8080 0.0.0.0:*\n"),
	} {
		t.Run(name, func(t *testing.T) {
			allowed, err := tcpPortRestrictedToLoopback(output, "5432")
			if err != nil || allowed {
				t.Fatalf("non-loopback listener outcome: allowed=%v error=%v", allowed, err)
			}
		})
	}

	for name, output := range map[string][]byte{
		"short row":    []byte("LISTEN broken\n"),
		"mapped IPv4":  []byte("LISTEN 0 128 [::ffff:127.0.0.1]:5432 [::]:*\n"),
		"zoned IPv6":   []byte("LISTEN 0 128 [fe80::1%eth0]:5432 [::]:*\n"),
		"invalid port": []byte("LISTEN 0 128 127.0.0.1:not-a-port 0.0.0.0:*\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if allowed, err := tcpPortRestrictedToLoopback(output, "5432"); err == nil || allowed {
				t.Fatalf("malformed listener output did not fail closed: allowed=%v error=%v", allowed, err)
			}
		})
	}

	runner := &fakeRunner{listeners: []byte("LISTEN 0 128 192.168.33.253:5432 0.0.0.0:*\n")}
	finalizer := &Finalizer{config: Config{Runner: runner}, platform: testPlatformContract(t, testPlatformID)}
	err := finalizer.startLocalPostgres(context.Background())
	commands := strings.Join(runner.commands, "\n")
	if err == nil || !strings.Contains(err.Error(), "not restricted") {
		t.Fatalf("startLocalPostgres accepted an external listener: %v", err)
	}
	if !strings.Contains(commands, "/usr/bin/systemctl start postgresql.service") ||
		!strings.Contains(commands, "/usr/bin/systemctl is-active --quiet postgresql.service") ||
		strings.Contains(commands, "enable --now postgresql.service") {
		t.Fatalf("PostgreSQL startup crossed its systemd boundary: %s", commands)
	}
}

func commandIndexContaining(commands []string, fragment string) int {
	for index, command := range commands {
		if strings.Contains(command, fragment) {
			return index
		}
	}
	return -1
}

func mkdirAllWithMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(path, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
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
		NginxWantsLink:  filepath.Join(base, "etc", "systemd", "system", "multi-user.target.wants", "nginx.service"),
		NginxNativeUnit: filepath.Join(base, "usr", "lib", "systemd", "system", "nginx.service"),
		TLSRoot:         filepath.Join(base, "etc", "probe-panel", "tls"), LetsEncryptLive: filepath.Join(base, "etc", "letsencrypt", "live", "probe-panel"),
		PrivateTLSRoot:        filepath.Join(base, "etc", "probe-panel", "tls", "private-ca"),
		PrivateCACertificate:  filepath.Join(base, "etc", "probe-panel", "tls", "private-ca", "ca.pem"),
		PrivateCAKey:          filepath.Join(base, "etc", "probe-panel", "tls", "private-ca", "ca-key.pem"),
		PrivateCertificate:    filepath.Join(base, "etc", "probe-panel", "tls", "private-ca", "fullchain.pem"),
		PrivateKey:            filepath.Join(base, "etc", "probe-panel", "tls", "private-ca", "privkey.pem"),
		LetsEncryptArchive:    filepath.Join(base, "etc", "letsencrypt", "archive", "probe-panel"),
		LetsEncryptRenewal:    filepath.Join(base, "etc", "letsencrypt", "renewal", "probe-panel.conf"),
		LetsEncryptDeployHook: filepath.Join(base, "etc", "letsencrypt", "renewal-hooks", "deploy", "probe-panel"),
		APIUnit:               filepath.Join(base, "etc", "systemd", "system", "probe-api.service"),
		BackupUnit:            filepath.Join(base, "etc", "systemd", "system", "probe-postgres-backup.service"),
		BackupTimerUnit:       filepath.Join(base, "etc", "systemd", "system", "probe-postgres-backup.timer"),
	}
}

func writeTestBundle(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"BUNDLE-SHA256SUMS":                                           "placeholder\n",
		"RELEASE-MANIFEST":                                            "runtime_abi=" + RuntimeABISystemdV2 + "\nplatform_ids=" + SupportedPlatformIDs + "\n",
		"artifacts/api/probe-api":                                     "binary\n",
		"source/probe-api/deploy/scripts/deploy-common.sh":            "#!/bin/sh\nexit 0\n",
		"source/probe-api/deploy/scripts/install-release.sh":          "#!/bin/sh\nexit 0\n",
		"source/probe-api/deploy/nginx/nginx.conf":                    "panel.example.com admin.example.com api.example.com\n",
		"source/probe-api/deploy/nginx/nginx-management.conf":         "admin.example.com\n",
		"source/probe-api/deploy/nginx/nginx-management-legacy.conf":  "admin.example.com\n",
		"source/probe-api/deploy/nginx/nginx-management-classic.conf": "admin.example.com\n",
		"source/probe-api/deploy/nginx/nginx-ip.conf": strings.Join([]string{
			"PROBE_SETUP_SERVER_IP:18453", "PROBE_SETUP_SERVER_IP:18454", "PROBE_SETUP_SERVER_IP:18455", "",
		}, "\n"),
		"source/probe-api/deploy/nginx/nginx-management-ip.conf":                        "PROBE_SETUP_SERVER_IP:18455\n",
		"source/probe-api/deploy/nginx/nginx-management-ip-legacy.conf":                 "PROBE_SETUP_SERVER_IP:18455\n",
		"source/probe-api/deploy/nginx/nginx-management-ip-classic.conf":                "PROBE_SETUP_SERVER_IP:18455\n",
		"source/probe-api/deploy/setup/probe-panel-setup.service":                       "[Service]\n",
		"source/probe-api/deploy/setup/probe-panel-setup.socket":                        "[Socket]\n",
		"source/probe-api/deploy/setup/probe-panel-finalizer.path":                      "[Path]\n",
		"source/probe-api/deploy/setup/probe-panel-finalizer-management.service":        "[Service]\n",
		"source/probe-api/deploy/systemd/probe-api.service":                             "[Service]\n",
		"source/probe-api/deploy/systemd/probe-postgres-backup.service":                 "[Service]\n",
		"source/probe-api/deploy/systemd/probe-postgres-backup.timer":                   "[Timer]\n",
		"source/probe-api/deploy/setup/probe-panel-setup-legacy.service":                "[Service]\n",
		"source/probe-api/deploy/setup/probe-panel-setup-legacy.socket":                 "[Socket]\n",
		"source/probe-api/deploy/setup/probe-panel-finalizer-management-legacy.service": "[Service]\n",
		"source/probe-api/deploy/systemd/probe-api-legacy.service":                      "[Service]\n",
		"source/probe-api/deploy/systemd/probe-postgres-backup-legacy.service":          "[Service]\n",
		"source/probe-api/deploy/systemd/probe-postgres-backup-legacy.timer":            "[Timer]\n",
		"source/probe-api/config/probe-api.env.example": strings.Join([]string{
			"PROBE_API_LISTEN_ADDR=127.0.0.1:8080", "PROBE_DATABASE_URL=postgresql://placeholder",
			"PROBE_PLATFORM_ID=debian-13-systemd",
			"PROBE_INSTALLATION_PROFILE=full",
			"PROBE_INGRESS_MODE=domain",
			"PROBE_ADMIN_ORIGIN=https://admin.example.com", "PROBE_AGENT_PUBLIC_URL=https://api.example.com",
			"PROBE_AGENT_INSTALLER_URL=https://raw.githubusercontent.com/Kcmose/my-agent/refs/tags/v1.0.2/deploy/install.sh",
			"PROBE_AGENT_INSTALL_CA_FILE=",
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
