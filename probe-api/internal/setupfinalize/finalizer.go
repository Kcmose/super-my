package setupfinalize

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"probe-api/internal/setup"
)

const verifiedAgentInstallerURL = "https://raw.githubusercontent.com/Kcmose/my-agent/refs/tags/v1.0.1/deploy/install.sh"

// Paths is intentionally explicit. Production uses DefaultPaths; tests use an
// isolated root without weakening any production path checks.
type Paths struct {
	ProbeRoot             string
	APIPath               string
	AgentPath             string
	WebPath               string
	AdminPath             string
	MigrationsPath        string
	ConfigDir             string
	NginxConfigDir        string
	BackupScriptDir       string
	ReleaseDir            string
	BackupDir             string
	PostgresBackupDir     string
	APIEnvironment        string
	BackupEnvironment     string
	PGPass                string
	Allowlist             string
	ActiveNginxConfig     string
	NginxLink             string
	NginxConfD            string
	NginxSitesEnabled     string
	TLSRoot               string
	LetsEncryptLive       string
	LetsEncryptDeployHook string
	APIUnit               string
	BackupUnit            string
	BackupTimerUnit       string
}

func DefaultPaths() Paths {
	return Paths{
		ProbeRoot:             "/srv/probe",
		APIPath:               "/srv/probe/api/probe-api",
		AgentPath:             "/srv/probe/agent",
		WebPath:               "/srv/probe/web",
		AdminPath:             "/srv/probe/admin",
		MigrationsPath:        "/srv/probe/migrations",
		ConfigDir:             "/srv/probe/config",
		NginxConfigDir:        "/srv/probe/config/nginx",
		BackupScriptDir:       "/srv/probe/api/scripts",
		ReleaseDir:            "/srv/probe/releases",
		BackupDir:             "/srv/probe/backups",
		PostgresBackupDir:     "/var/backups/probe-panel/postgres",
		APIEnvironment:        "/srv/probe/config/probe-api.env",
		BackupEnvironment:     "/srv/probe/config/probe-postgres-backup.env",
		PGPass:                "/srv/probe/config/probe-postgres.pgpass",
		Allowlist:             "/etc/probe-panel/admin-allowlist.geo",
		ActiveNginxConfig:     "/srv/probe/config/nginx/nginx.conf",
		NginxLink:             "/etc/nginx/conf.d/probe-panel.conf",
		NginxConfD:            "/etc/nginx/conf.d",
		NginxSitesEnabled:     "/etc/nginx/sites-enabled",
		TLSRoot:               "/etc/probe-panel/tls",
		LetsEncryptLive:       "/etc/letsencrypt/live/probe-panel",
		LetsEncryptDeployHook: "/etc/letsencrypt/renewal-hooks/deploy/probe-panel",
		APIUnit:               "/etc/systemd/system/probe-api.service",
		BackupUnit:            "/etc/systemd/system/probe-postgres-backup.service",
		BackupTimerUnit:       "/etc/systemd/system/probe-postgres-backup.timer",
	}
}

type Identity struct {
	UID int
	GID int
}

type IdentityLookup func(string) (Identity, error)

type Config struct {
	BundleRoot      string
	ReleaseID       string
	Paths           Paths
	Runner          Runner
	Bootstrapper    ApplicationBootstrapper
	IdentityLookup  IdentityLookup
	RootIdentity    Identity
	RequireRoot     bool
	Now             func() time.Time
	ResolveHostname func(context.Context, string) error
}

type Finalizer struct {
	config Config
}

func New(config Config) (*Finalizer, error) {
	if config.Paths == (Paths{}) {
		config.Paths = DefaultPaths()
	}
	if config.Runner == nil {
		config.Runner = OSRunner{}
	}
	if config.Bootstrapper == nil {
		config.Bootstrapper = PostgresApplicationBootstrapper{}
	}
	if config.IdentityLookup == nil {
		config.IdentityLookup = lookupIdentity
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.ResolveHostname == nil {
		config.ResolveHostname = resolveHostname
	}
	if config.BundleRoot == "" || !filepath.IsAbs(config.BundleRoot) || filepath.Clean(config.BundleRoot) != config.BundleRoot {
		return nil, errors.New("setup release bundle root must be an absolute canonical path")
	}
	if config.ReleaseID == "" || len(config.ReleaseID) > 96 || !safeReleaseID(config.ReleaseID) {
		return nil, errors.New("setup release identifier is invalid")
	}
	if err := validatePaths(config.Paths); err != nil {
		return nil, err
	}
	return &Finalizer{config: config}, nil
}

func (finalizer *Finalizer) Finalize(ctx context.Context, request setup.CompleteRequest) (err error) {
	if finalizer == nil {
		return errors.New("setup production finalizer is not configured")
	}
	defer request.ClearSecrets()
	if finalizer.config.RequireRoot && os.Geteuid() != 0 {
		return errors.New("setup production finalizer must run as root")
	}
	if err := finalizer.requireIdentityCapabilities("entry"); err != nil {
		return err
	}
	if err := request.Validate(); err != nil {
		return fmt.Errorf("validate setup request: %w", err)
	}
	if err := validateProductionRequest(request); err != nil {
		return err
	}
	if err := finalizer.validateBundle(); err != nil {
		return err
	}
	if err := finalizer.validateFreshIngress(); err != nil {
		return err
	}
	for _, hostname := range []string{request.Domains.Panel, request.Domains.Admin, request.Domains.Agent} {
		if err := finalizer.config.ResolveHostname(ctx, hostname); err != nil {
			return fmt.Errorf("resolve configured hostname %s: %w", hostname, err)
		}
	}
	if err := finalizer.requireIdentityCapabilities("preflight"); err != nil {
		return err
	}

	// From this point this process owns the otherwise-clean Probe ingress. Any
	// failure keeps the public API and Nginx stopped while retaining database
	// and certificate material for an explicit recovery workflow.
	ownedTarget := true
	defer func() {
		if err != nil && ownedTarget {
			finalizer.stopProduction()
		}
	}()

	identity, err := finalizer.ensureServiceIdentity(ctx)
	if err != nil {
		return err
	}
	if err := finalizer.requireIdentityCapabilities("service identity lookup"); err != nil {
		return err
	}
	if err := finalizer.prepareLayout(identity); err != nil {
		return err
	}
	if err := finalizer.requireIdentityCapabilities("filesystem preparation"); err != nil {
		return err
	}
	if err := finalizer.startLocalPostgres(ctx); err != nil {
		return err
	}
	if err := finalizer.requireIdentityCapabilities("PostgreSQL startup"); err != nil {
		return err
	}
	if err := finalizer.createDatabase(ctx, request); err != nil {
		return err
	}
	databaseURL := postgresURL(request)
	if err := finalizer.writeConfiguration(request, databaseURL, identity); err != nil {
		return err
	}
	if err := finalizer.issueCertificate(ctx, request); err != nil {
		return err
	}

	databasePassword := append([]byte(nil), request.Database.Password...)
	defer clear(databasePassword)
	adminPassword := append([]byte(nil), request.Administrator.Password...)
	defer clear(adminPassword)
	if err := finalizer.config.Bootstrapper.MigrateAndBootstrap(
		ctx,
		DatabaseConfig{
			Name: request.Database.Name, Username: request.Database.Username,
			Password: databasePassword,
		},
		request.Administrator.Username,
		adminPassword,
	); err != nil {
		return fmt.Errorf("initialize application database and administrator: %w", err)
	}

	installScript := filepath.Join(finalizer.config.BundleRoot, "source/probe-api/deploy/scripts/install-release.sh")
	if err := finalizer.config.Runner.Run(ctx, installScript,
		"--bundle-root", finalizer.config.BundleRoot,
		"--release-id", finalizer.config.ReleaseID,
		"--disable-default-site",
	); err != nil {
		return fmt.Errorf("activate verified release: %w", err)
	}
	if err := finalizer.config.Runner.Run(ctx, "/usr/bin/systemctl", "enable", "--now", "certbot.timer"); err != nil {
		return fmt.Errorf("enable certificate renewal timer: %w", err)
	}
	if err := finalizer.config.Runner.Run(ctx, "/usr/bin/systemctl", "disable", "probe-panel-setup.service", "probe-panel-finalizer.path"); err != nil {
		return fmt.Errorf("disable first-run services: %w", err)
	}
	if err := finalizer.config.Runner.Run(ctx, "/usr/bin/systemctl", "stop", "probe-panel-finalizer.path"); err != nil {
		return fmt.Errorf("stop first-run finalizer trigger: %w", err)
	}
	ownedTarget = false
	return nil
}

func (finalizer *Finalizer) validateBundle() error {
	rootInfo, err := os.Lstat(finalizer.config.BundleRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("verified setup release bundle is unavailable")
	}
	required := []string{
		"BUNDLE-SHA256SUMS",
		"artifacts/api/probe-api",
		"source/probe-api/config/probe-api.env.example",
		"source/probe-api/config/probe-postgres-backup.env.example",
		"source/probe-api/deploy/nginx/nginx.conf",
		"source/probe-api/deploy/scripts/install-release.sh",
	}
	for _, relative := range required {
		path := filepath.Join(finalizer.config.BundleRoot, filepath.FromSlash(relative))
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("verified setup release is incomplete: %s", relative)
		}
	}
	return nil
}

func (finalizer *Finalizer) validateFreshIngress() error {
	paths := finalizer.config.Paths
	for _, path := range []string{
		paths.APIPath, paths.AgentPath, paths.WebPath, paths.AdminPath, paths.MigrationsPath,
		paths.APIEnvironment, paths.BackupEnvironment, paths.PGPass, paths.Allowlist,
		paths.ActiveNginxConfig, paths.NginxLink, paths.LetsEncryptLive,
		paths.APIUnit, paths.BackupUnit, paths.BackupTimerUnit,
	} {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("existing production asset prevents first installation: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect first-install target %s: %w", path, err)
		}
	}
	if err := validateNginxDirectory(paths.NginxConfD, ""); err != nil {
		return err
	}
	if err := validateNginxDirectory(paths.NginxSitesEnabled, "default"); err != nil {
		return err
	}
	return nil
}

func (finalizer *Finalizer) ensureServiceIdentity(ctx context.Context) (Identity, error) {
	identity, err := finalizer.config.IdentityLookup("probe-api")
	if err == nil {
		return identity, nil
	}
	if err := finalizer.config.Runner.Run(ctx, "/usr/sbin/addgroup", "--system", "probe-api"); err != nil {
		return Identity{}, fmt.Errorf("create probe-api group: %w", err)
	}
	if err := finalizer.config.Runner.Run(ctx, "/usr/sbin/adduser", "--system", "--ingroup", "probe-api", "--no-create-home", "--home", "/nonexistent", "--shell", "/usr/sbin/nologin", "probe-api"); err != nil {
		return Identity{}, fmt.Errorf("create probe-api user: %w", err)
	}
	identity, err = finalizer.config.IdentityLookup("probe-api")
	if err != nil {
		return Identity{}, errors.New("probe-api service account is unavailable after creation")
	}
	return identity, nil
}

func (finalizer *Finalizer) prepareLayout(identity Identity) error {
	root := finalizer.config.RootIdentity
	paths := finalizer.config.Paths
	directories := []directorySpec{
		{paths.ProbeRoot, 0o755, root},
		{filepath.Dir(paths.PostgresBackupDir), 0o755, root},
		{filepath.Dir(paths.Allowlist), 0o750, Identity{UID: 0, GID: identity.GID}},
		{paths.ConfigDir, 0o750, Identity{UID: 0, GID: identity.GID}},
		{paths.NginxConfigDir, 0o755, root},
		{filepath.Dir(paths.APIPath), 0o755, root},
		{paths.BackupScriptDir, 0o750, Identity{UID: 0, GID: identity.GID}},
		{paths.ReleaseDir, 0o755, root},
		{paths.BackupDir, 0o700, root},
		{paths.PostgresBackupDir, 0o700, identity},
		{paths.TLSRoot, 0o755, root},
		{filepath.Join(paths.TLSRoot, "panel"), 0o755, root},
		{filepath.Join(paths.TLSRoot, "admin"), 0o755, root},
		{filepath.Join(paths.TLSRoot, "api"), 0o755, root},
		{filepath.Dir(filepath.Dir(filepath.Dir(paths.LetsEncryptDeployHook))), 0o755, root},
		{filepath.Dir(filepath.Dir(paths.LetsEncryptDeployHook)), 0o755, root},
		{filepath.Dir(paths.LetsEncryptDeployHook), 0o755, root},
	}
	for _, directory := range directories {
		if err := ensureDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func (finalizer *Finalizer) startLocalPostgres(ctx context.Context) error {
	if err := finalizer.config.Runner.Run(ctx, "/usr/bin/systemctl", "enable", "--now", "postgresql.service"); err != nil {
		return fmt.Errorf("start local PostgreSQL: %w", err)
	}
	var ready bool
	for attempt := 0; attempt < 20; attempt++ {
		if err := finalizer.config.Runner.RunQuiet(ctx, "/usr/bin/pg_isready", "--host", "127.0.0.1", "--port", "5432", "--timeout", "2"); err == nil {
			ready = true
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if !ready {
		return errors.New("local PostgreSQL did not become ready")
	}
	listeners, err := finalizer.config.Runner.Output(ctx, "/usr/bin/ss", "-H", "-lnt")
	if err != nil {
		return fmt.Errorf("inspect PostgreSQL listener: %w", err)
	}
	if wildcardListener(listeners, "5432") {
		return errors.New("PostgreSQL port 5432 is exposed beyond loopback")
	}
	return nil
}

func (finalizer *Finalizer) createDatabase(ctx context.Context, request setup.CompleteRequest) error {
	roleExists, err := finalizer.postgresObjectExists(ctx, "pg_roles", "rolname", request.Database.Username)
	if err != nil {
		return err
	}
	databaseExists, err := finalizer.postgresObjectExists(ctx, "pg_database", "datname", request.Database.Name)
	if err != nil {
		return err
	}
	if roleExists || databaseExists {
		return errors.New("selected PostgreSQL role or database already exists; first installation will not overwrite it")
	}
	roleSQL := "SET standard_conforming_strings = on;\nCREATE ROLE " + quoteIdentifier(request.Database.Username) + " LOGIN PASSWORD " + quoteLiteral(request.Database.Password) + ";\n"
	roleBytes := []byte(roleSQL)
	defer clear(roleBytes)
	if err := finalizer.config.Runner.RunSensitive(ctx, roleBytes, "/usr/bin/setpriv", postgresCommand("--no-psqlrc", "--set=ON_ERROR_STOP=1", "--dbname=postgres", "--quiet")...); err != nil {
		return fmt.Errorf("create dedicated PostgreSQL role: %w", err)
	}
	databaseSQL := []byte("CREATE DATABASE " + quoteIdentifier(request.Database.Name) + " OWNER " + quoteIdentifier(request.Database.Username) + ";\n")
	defer clear(databaseSQL)
	if err := finalizer.config.Runner.RunSensitive(ctx, databaseSQL, "/usr/bin/setpriv", postgresCommand("--no-psqlrc", "--set=ON_ERROR_STOP=1", "--dbname=postgres", "--quiet")...); err != nil {
		return fmt.Errorf("create dedicated PostgreSQL database: %w", err)
	}
	return nil
}

func (finalizer *Finalizer) postgresObjectExists(ctx context.Context, table, column, value string) (bool, error) {
	query := "SELECT CASE WHEN EXISTS (SELECT 1 FROM " + table + " WHERE " + column + " = " + quoteLiteral([]byte(value)) + ") THEN 'yes' ELSE 'no' END;"
	output, err := finalizer.config.Runner.Output(ctx, "/usr/bin/setpriv", postgresCommand("--no-psqlrc", "--tuples-only", "--no-align", "--dbname=postgres", "--command", query)...)
	if err != nil {
		return false, errors.New("inspect local PostgreSQL catalog")
	}
	switch strings.TrimSpace(string(output)) {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	default:
		return false, errors.New("local PostgreSQL returned an unexpected catalog result")
	}
}

func postgresCommand(arguments ...string) []string {
	prefix := []string{
		"--reuid=postgres", "--regid=postgres", "--init-groups",
		"--reset-env", "--", "/usr/bin/setpriv",
		"--inh-caps=-all", "--ambient-caps=-all",
		"--", "/usr/bin/psql",
	}
	return append(prefix, arguments...)
}

func (finalizer *Finalizer) requireIdentityCapabilities(stage string) error {
	if !finalizer.config.RequireRoot {
		return nil
	}
	contents, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return fmt.Errorf("inspect setup finalizer capabilities at %s", stage)
	}
	for _, line := range strings.Split(string(contents), "\n") {
		if !strings.HasPrefix(line, "CapEff:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "CapEff:"))
		effective, parseErr := strconv.ParseUint(value, 16, 64)
		if parseErr != nil {
			break
		}
		const required = uint64(1<<6 | 1<<7) // CAP_SETGID | CAP_SETUID
		if effective&required == required {
			return nil
		}
		return fmt.Errorf("setup finalizer lost required identity capabilities after %s", stage)
	}
	return fmt.Errorf("inspect setup finalizer capabilities at %s", stage)
}

func (finalizer *Finalizer) writeConfiguration(request setup.CompleteRequest, databaseURL string, identity Identity) error {
	root := finalizer.config.RootIdentity
	apiExample, err := readSmallRegular(filepath.Join(finalizer.config.BundleRoot, "source/probe-api/config/probe-api.env.example"), 128*1024)
	if err != nil {
		return err
	}
	apiValues := map[string]string{
		"PROBE_DATABASE_URL":         databaseURL,
		"PROBE_ADMIN_ORIGIN":         "https://" + request.Domains.Admin,
		"PROBE_AGENT_PUBLIC_URL":     "https://" + request.Domains.Agent,
		"PROBE_AGENT_INSTALLER_URL":  verifiedAgentInstallerURL,
		"PROBE_ADMIN_ALLOWLIST_FILE": finalizer.config.Paths.Allowlist,
		"PROBE_TRUSTED_PROXY_CIDRS":  "127.0.0.1/32,::1/128",
		"PROBE_API_LISTEN_ADDR":      "127.0.0.1:8080",
	}
	apiEnvironment, err := replaceEnvironment(apiExample, apiValues)
	clear(apiExample)
	if err != nil {
		return err
	}
	defer clear(apiEnvironment)

	backupExample, err := readSmallRegular(filepath.Join(finalizer.config.BundleRoot, "source/probe-api/config/probe-postgres-backup.env.example"), 32*1024)
	if err != nil {
		return err
	}
	backupEnvironment, err := replaceEnvironment(backupExample, map[string]string{
		"PGHOST": "127.0.0.1", "PGPORT": "5432",
		"PGDATABASE": request.Database.Name, "PGUSER": request.Database.Username,
		"PGPASSFILE":                finalizer.config.Paths.PGPass,
		"PROBE_POSTGRES_BACKUP_DIR": finalizer.config.Paths.PostgresBackupDir,
	})
	clear(backupExample)
	if err != nil {
		return err
	}
	defer clear(backupEnvironment)

	pgpass := []byte("127.0.0.1:5432:" + escapePGPass(request.Database.Name) + ":" + escapePGPass(request.Database.Username) + ":" + escapePGPass(string(request.Database.Password)) + "\n")
	defer clear(pgpass)
	allowlist, err := renderAllowlist(request.Allowlist)
	if err != nil {
		return err
	}
	nginxTemplate, err := readSmallRegular(filepath.Join(finalizer.config.BundleRoot, "source/probe-api/deploy/nginx/nginx.conf"), 1024*1024)
	if err != nil {
		return err
	}
	nginxConfig, err := renderNginx(nginxTemplate, request.Domains)
	clear(nginxTemplate)
	if err != nil {
		return err
	}

	files := []fileSpec{
		{finalizer.config.Paths.APIEnvironment, apiEnvironment, 0o640, Identity{UID: 0, GID: identity.GID}},
		{finalizer.config.Paths.BackupEnvironment, backupEnvironment, 0o600, root},
		{finalizer.config.Paths.PGPass, pgpass, 0o600, identity},
		{finalizer.config.Paths.Allowlist, allowlist, 0o640, Identity{UID: 0, GID: identity.GID}},
		{finalizer.config.Paths.ActiveNginxConfig, nginxConfig, 0o644, root},
	}
	for _, file := range files {
		if err := createFileAtomic(file); err != nil {
			return err
		}
	}
	return nil
}

func (finalizer *Finalizer) issueCertificate(ctx context.Context, request setup.CompleteRequest) error {
	if err := finalizer.config.Runner.Run(ctx, "/usr/bin/systemctl", "stop", "nginx.service"); err != nil {
		return fmt.Errorf("stop Nginx before ACME challenge: %w", err)
	}
	listeners, err := finalizer.config.Runner.Output(ctx, "/usr/bin/ss", "-H", "-lnt")
	if err != nil {
		return fmt.Errorf("inspect ACME ports: %w", err)
	}
	if anyPortListener(listeners, "80") || anyPortListener(listeners, "443") {
		return errors.New("TCP port 80 or 443 is already in use; refusing to interrupt an unrelated service")
	}
	if err := finalizer.config.Runner.Run(ctx, "/usr/bin/certbot",
		"certonly", "--standalone", "--non-interactive", "--agree-tos",
		"--email", request.TLS.Email, "--cert-name", "probe-panel",
		"--preferred-challenges", "http",
		"--domain", request.Domains.Panel,
		"--domain", request.Domains.Admin,
		"--domain", request.Domains.Agent,
	); err != nil {
		return fmt.Errorf("obtain ACME certificate: %w", err)
	}
	certificatePath := filepath.Join(finalizer.config.Paths.LetsEncryptLive, "fullchain.pem")
	privateKeyPath := filepath.Join(finalizer.config.Paths.LetsEncryptLive, "privkey.pem")
	if err := validateCertificate(certificatePath, privateKeyPath, request.Domains, finalizer.config.Now()); err != nil {
		return err
	}
	for _, surface := range []string{"panel", "admin", "api"} {
		if err := createAbsoluteSymlink(certificatePath, filepath.Join(finalizer.config.Paths.TLSRoot, surface, "fullchain.pem")); err != nil {
			return err
		}
		if err := createAbsoluteSymlink(privateKeyPath, filepath.Join(finalizer.config.Paths.TLSRoot, surface, "privkey.pem")); err != nil {
			return err
		}
	}
	hook := []byte("#!/bin/sh\nset -eu\n/usr/sbin/nginx -t\n/usr/bin/systemctl reload nginx.service\n")
	if err := createFileAtomic(fileSpec{finalizer.config.Paths.LetsEncryptDeployHook, hook, 0o755, finalizer.config.RootIdentity}); err != nil {
		return err
	}
	return nil
}

func (finalizer *Finalizer) stopProduction() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = finalizer.config.Runner.RunQuiet(ctx, "/usr/bin/systemctl", "stop", "probe-api.service", "probe-postgres-backup.timer", "nginx.service")
}

func validateProductionRequest(request setup.CompleteRequest) error {
	if request.Database.Name == "postgres" || request.Database.Name == "template0" || request.Database.Name == "template1" || request.Database.Username == "postgres" {
		return errors.New("reserved PostgreSQL role or database name is not allowed")
	}
	domains := []string{request.Domains.Panel, request.Domains.Admin, request.Domains.Agent}
	for _, domain := range domains {
		if domain == "example.com" || strings.HasSuffix(domain, ".example.com") {
			return errors.New("example.com hostnames cannot be used for production installation")
		}
	}
	for index := range domains {
		for other := range domains {
			if index != other && strings.Contains(domains[index], domains[other]) {
				return errors.New("one production hostname must not contain another hostname")
			}
		}
	}
	for _, character := range request.Database.Password {
		if character < 0x20 || character == 0x7f {
			return errors.New("database password must not contain control characters")
		}
	}
	return nil
}

func postgresURL(request setup.CompleteRequest) string {
	value := &url.URL{
		Scheme:   "postgresql",
		User:     url.UserPassword(request.Database.Username, string(request.Database.Password)),
		Host:     "127.0.0.1:5432",
		Path:     "/" + request.Database.Name,
		RawQuery: "sslmode=disable",
	}
	return value.String()
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func quoteLiteral(value []byte) string {
	return "'" + strings.ReplaceAll(string(value), "'", "''") + "'"
}

func escapePGPass(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, ":", `\:`)
}

func renderAllowlist(values []string) ([]byte, error) {
	seen := make(map[netip.Prefix]struct{}, len(values))
	var builder strings.Builder
	for _, value := range values {
		var prefix netip.Prefix
		if parsed, err := netip.ParsePrefix(value); err == nil {
			prefix = parsed.Masked()
		} else if address, addressErr := netip.ParseAddr(value); addressErr == nil && address.IsValid() && !address.Is4In6() && address.Zone() == "" {
			prefix = netip.PrefixFrom(address, address.BitLen())
		} else {
			return nil, errors.New("allowlist contains an invalid IP or CIDR")
		}
		if prefix.Bits() == 0 || prefix.Addr().Is4In6() || prefix.Addr().Zone() != "" {
			return nil, errors.New("allowlist contains a forbidden default or mapped prefix")
		}
		if _, duplicate := seen[prefix]; duplicate {
			return nil, errors.New("allowlist contains a duplicate prefix")
		}
		seen[prefix] = struct{}{}
		builder.WriteString(prefix.String())
		builder.WriteString(" 1;\n")
	}
	if len(seen) == 0 {
		return nil, errors.New("allowlist must not be empty")
	}
	return []byte(builder.String()), nil
}

func renderNginx(template []byte, domains setup.DomainInput) ([]byte, error) {
	contents := string(template)
	replacements := []struct{ placeholder, replacement string }{
		{"panel.example.com", domains.Panel},
		{"admin.example.com", domains.Admin},
		{"api.example.com", domains.Agent},
	}
	for _, item := range replacements {
		placeholder, replacement := item.placeholder, item.replacement
		if !strings.Contains(contents, placeholder) {
			return nil, fmt.Errorf("Nginx template is missing %s", placeholder)
		}
		contents = strings.ReplaceAll(contents, placeholder, replacement)
	}
	if strings.Contains(contents, ".example.com") {
		return nil, errors.New("Nginx template still contains an example hostname")
	}
	return []byte(contents), nil
}

func replaceEnvironment(example []byte, replacements map[string]string) ([]byte, error) {
	for key, value := range replacements {
		if value == "" || strings.ContainsAny(value, " \t\r\n") {
			return nil, fmt.Errorf("environment value for %s is unsafe", key)
		}
	}
	lines := strings.Split(strings.ReplaceAll(string(example), "\r\n", "\n"), "\n")
	seen := make(map[string]int, len(replacements))
	for index, line := range lines {
		for key, value := range replacements {
			if strings.HasPrefix(line, key+"=") {
				seen[key]++
				lines[index] = key + "=" + value
			}
		}
	}
	for key := range replacements {
		if seen[key] != 1 {
			return nil, fmt.Errorf("environment template must contain exactly one %s", key)
		}
	}
	return []byte(strings.Join(lines, "\n")), nil
}

func validateCertificate(certificatePath, privateKeyPath string, domains setup.DomainInput, now time.Time) error {
	pair, err := tls.LoadX509KeyPair(certificatePath, privateKeyPath)
	if err != nil || len(pair.Certificate) == 0 {
		return errors.New("ACME certificate and private key are invalid or mismatched")
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return errors.New("ACME leaf certificate cannot be parsed")
	}
	if !now.Before(certificate.NotAfter) {
		return errors.New("ACME certificate is already expired")
	}
	for _, hostname := range []string{domains.Panel, domains.Admin, domains.Agent} {
		if err := certificate.VerifyHostname(hostname); err != nil {
			return fmt.Errorf("ACME certificate does not cover %s", hostname)
		}
	}
	return nil
}

func validateNginxDirectory(path, allowed string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("inspect existing Nginx configuration %s: %w", path, err)
	}
	for _, entry := range entries {
		if allowed != "" && entry.Name() == allowed {
			continue
		}
		return fmt.Errorf("existing Nginx configuration prevents first installation: %s", filepath.Join(path, entry.Name()))
	}
	return nil
}

func validatePaths(paths Paths) error {
	for _, path := range []string{
		paths.ProbeRoot, paths.APIPath, paths.AgentPath, paths.WebPath, paths.AdminPath,
		paths.MigrationsPath, paths.ConfigDir, paths.NginxConfigDir, paths.BackupScriptDir,
		paths.ReleaseDir, paths.BackupDir, paths.PostgresBackupDir, paths.APIEnvironment,
		paths.BackupEnvironment, paths.PGPass, paths.Allowlist, paths.ActiveNginxConfig,
		paths.NginxLink, paths.NginxConfD, paths.NginxSitesEnabled, paths.TLSRoot,
		paths.LetsEncryptLive, paths.LetsEncryptDeployHook, paths.APIUnit,
		paths.BackupUnit, paths.BackupTimerUnit,
	} {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("setup finalizer paths must be absolute and canonical")
		}
	}
	return nil
}

func safeReleaseID(value string) bool {
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || (index > 0 && (character == '.' || character == '_' || character == '-')) {
			continue
		}
		return false
	}
	return true
}

func lookupIdentity(name string) (Identity, error) {
	account, err := user.Lookup(name)
	if err != nil {
		return Identity{}, err
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return Identity{}, err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return Identity{}, err
	}
	return Identity{UID: uid, GID: gid}, nil
}

func resolveHostname(ctx context.Context, hostname string) error {
	// LookupNetIP avoids accepting a search-domain expansion and does not
	// compare addresses with local interfaces, which would break valid NAT.
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", hostname)
	if err != nil || len(addresses) == 0 {
		return errors.New("DNS name has no address record")
	}
	return nil
}

func newRequestID(now time.Time) (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", errors.New("generate setup administrator request ID")
	}
	defer clear(value)
	return "local-setup-" + now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(value), nil
}
