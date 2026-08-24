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
	listeners       []byte
	listenerError   error
	commands        []string
	sensitiveInputs [][]byte
	commandErrors   map[string]error
	outputValues    map[string][]byte
}

func (runner *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	command := name + " " + strings.Join(args, " ")
	runner.commands = append(runner.commands, command)
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
	if filepathBase(name) == "setpriv" {
		return []byte("no\n"), nil
	}
	if filepathBase(name) == "systemctl" && len(args) == 4 && args[0] == "show" && args[2] == "--value" {
		if args[1] == "--property=ActiveState" {
			return []byte("inactive\n"), nil
		}
		if args[1] == "--property=UnitFileState" {
			return []byte("disabled\n"), nil
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
	if err := os.Chmod(filepath.Dir(paths.Allowlist), 0o700); err != nil {
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
		BundleRoot: bundle, ReleaseID: "v1.0.0", Paths: paths,
		Runner: runner, Bootstrapper: bootstrapper,
		IdentityLookup: func(string) (Identity, error) { return currentIdentity, nil },
		RootIdentity:   currentIdentity,
		RequireRoot:    false, Now: func() time.Time { return now },
		ResolveHostname: func(context.Context, string) error { return nil },
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
	certificateIndex := commandIndexContaining(runner.commands, "/usr/bin/certbot certonly")
	timerIndex := commandIndexContaining(runner.commands, "/usr/bin/systemctl enable --now certbot.timer")
	tlsValidationIndex := commandIndexContaining(runner.commands, filepath.Join(bundle, "artifacts", "api", "probe-api")+" config validate-ingress-tls domain "+strings.Join(domains, " "))
	activateIndex := commandIndexContaining(runner.commands, "/deploy/scripts/install-release.sh --bundle-root")
	disableSetupIndex := commandIndexContaining(runner.commands, "/usr/bin/systemctl disable probe-panel-setup.service")
	stopPathIndex := commandIndexContaining(runner.commands, "/usr/bin/systemctl stop probe-panel-finalizer.path")
	if certificateIndex < 0 || timerIndex <= certificateIndex || tlsValidationIndex <= timerIndex || commandsAtBootstrap != tlsValidationIndex+1 || activateIndex != commandsAtBootstrap || disableSetupIndex <= activateIndex || stopPathIndex <= disableSetupIndex {
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
	if err != nil || !strings.Contains(string(apiEnvironment), "PROBE_INGRESS_MODE=domain") || !strings.Contains(string(apiEnvironment), "PROBE_ADMIN_ORIGIN=https://admin.monitor.test") {
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
	if err := os.Chmod(filepath.Dir(paths.Allowlist), 0o700); err != nil {
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
		BundleRoot: bundle, ReleaseID: "v1.0.0", Paths: paths,
		Runner: runner, Bootstrapper: bootstrapper,
		IdentityLookup: func(string) (Identity, error) { return currentIdentity, nil },
		RootIdentity:   currentIdentity, RequireRoot: false, Now: func() time.Time { return now },
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
	if strings.Contains(commands, "/usr/bin/certbot ") || strings.Contains(commands, "enable --now certbot.timer") {
		t.Fatalf("IP installation invoked ACME provisioning: %s", commands)
	}
	for _, required := range []string{
		"systemctl disable --now certbot.timer",
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
	timerIndex := commandIndexContaining(runner.commands, "/usr/bin/systemctl disable --now certbot.timer")
	tlsValidationIndex := commandIndexContaining(runner.commands, filepath.Join(bundle, "artifacts", "api", "probe-api")+" config validate-ingress-tls ip 10.20.30.40")
	activateIndex := commandIndexContaining(runner.commands, "/deploy/scripts/install-release.sh --bundle-root")
	if postgresStartIndex < 0 || postgresActiveIndex <= postgresStartIndex || timerIndex < 0 || tlsValidationIndex <= timerIndex || commandsAtBootstrap != tlsValidationIndex+1 || activateIndex != commandsAtBootstrap || strings.Contains(commands, "enable --now postgresql.service") {
		t.Fatalf("PostgreSQL/timer/TLS validation/bootstrap/activation ordering is invalid: bootstrap=%d commands=%s", commandsAtBootstrap, commands)
	}

	apiEnvironment, err := os.ReadFile(paths.APIEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
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

func TestInstalledCommitFailureRollsBackAndVerifiesEveryFormalUnit(t *testing.T) {
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
	stopAPICommand := "/usr/bin/systemctl stop probe-api.service"
	nginxStateCommand := "/usr/bin/systemctl show --property=ActiveState --value nginx.service"
	runner := &fakeRunner{
		listeners:     []byte("LISTEN 0 128 127.0.0.1:5432 0.0.0.0:*\n"),
		commandErrors: map[string]error{stopAPICommand: errors.New("simulated stop failure")},
		outputValues:  map[string][]byte{nginxStateCommand: []byte("active\n")},
	}
	bootstrapper := &fakeBootstrapper{}
	currentIdentity := Identity{UID: os.Getuid(), GID: os.Getgid()}
	commandsAtCommit := -1
	var wantsLinkSetupError error
	finalizer, err := New(Config{
		BundleRoot: bundle, ReleaseID: "v1.0.0", Paths: paths,
		Runner: runner, Bootstrapper: bootstrapper,
		IdentityLookup: func(string) (Identity, error) { return currentIdentity, nil },
		RootIdentity:   currentIdentity, RequireRoot: false,
		Now:             func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) },
		ResolveHostname: func(context.Context, string) error { return nil },
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
		"/usr/bin/systemctl show --property=ActiveState --value probe-api.service",
		"/usr/bin/systemctl show --property=ActiveState --value probe-postgres-backup.timer",
		"/usr/bin/systemctl show --property=ActiveState --value nginx.service",
		"/usr/bin/systemctl show --property=ActiveState --value certbot.timer",
		"/usr/bin/systemctl show --property=UnitFileState --value nginx.service",
	} {
		if commandIndexContaining(runner.commands, required) < commandsAtCommit {
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
		finalizer := &Finalizer{config: Config{Paths: paths, Runner: runner}}
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
			finalizer := &Finalizer{config: Config{Paths: paths, Runner: runner}}
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

func TestIPIngressPreflightRejectsEveryReservedPort(t *testing.T) {
	for _, port := range []string{"18453", "18454", "18455"} {
		t.Run(port, func(t *testing.T) {
			runner := &fakeRunner{listeners: []byte("LISTEN 0 128 0.0.0.0:" + port + " 0.0.0.0:*\n")}
			finalizer := &Finalizer{config: Config{Runner: runner}}
			if err := finalizer.validateAvailableIPPorts(context.Background()); err == nil || !strings.Contains(err.Error(), port) {
				t.Fatalf("occupied port %s was accepted: %v", port, err)
			}
		})
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
		BundleRoot: bundle, ReleaseID: "v1.0.0", Paths: paths,
		Runner: runner, Bootstrapper: bootstrapper,
		IdentityLookup: func(string) (Identity, error) {
			identityLookups++
			return currentIdentity, nil
		},
		RootIdentity: currentIdentity, RequireRoot: false,
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
			finalizer := &Finalizer{config: Config{Runner: runner}}
			if err := finalizer.validateAvailableIPPorts(context.Background()); err == nil {
				t.Fatal("unreliable ss result was accepted")
			}
		})
	}
	finalizer := &Finalizer{config: Config{Runner: &fakeRunner{listeners: []byte{}}}}
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
				BundleRoot: bundle, ReleaseID: "v1.0.0", Paths: paths,
				Runner: runner, Bootstrapper: bootstrapper,
				IdentityLookup: func(string) (Identity, error) {
					identityLookups++
					return currentIdentity, nil
				},
				RootIdentity: currentIdentity, RequireRoot: false,
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
	finalizer := &Finalizer{config: Config{Runner: runner}}
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
		"source/probe-api/deploy/nginx/nginx-ip.conf": strings.Join([]string{
			"PROBE_SETUP_SERVER_IP:18453", "PROBE_SETUP_SERVER_IP:18454", "PROBE_SETUP_SERVER_IP:18455", "",
		}, "\n"),
		"source/probe-api/config/probe-api.env.example": strings.Join([]string{
			"PROBE_API_LISTEN_ADDR=127.0.0.1:8080", "PROBE_DATABASE_URL=postgresql://placeholder",
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
