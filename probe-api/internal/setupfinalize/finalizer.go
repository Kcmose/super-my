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

const (
	verifiedAgentInstallerURL    = "https://raw.githubusercontent.com/Kcmose/my-agent/refs/tags/v1.0.2/deploy/install.sh"
	minimumPostgresVersionNumber = 140000
	maximumPostgresVersionNumber = 150000
	postgresCommandPath          = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

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
	NginxWantsLink        string
	NginxNativeUnit       string
	TLSRoot               string
	PrivateTLSRoot        string
	PrivateCACertificate  string
	PrivateCAKey          string
	PrivateCertificate    string
	PrivateKey            string
	LetsEncryptLive       string
	LetsEncryptArchive    string
	LetsEncryptRenewal    string
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
		NginxWantsLink:        "/etc/systemd/system/multi-user.target.wants/nginx.service",
		NginxNativeUnit:       "/usr/lib/systemd/system/nginx.service",
		TLSRoot:               "/etc/probe-panel/tls",
		PrivateTLSRoot:        "/etc/probe-panel/tls/private-ca",
		PrivateCACertificate:  "/etc/probe-panel/tls/private-ca/ca.pem",
		PrivateCAKey:          "/etc/probe-panel/tls/private-ca/ca-key.pem",
		PrivateCertificate:    "/etc/probe-panel/tls/private-ca/fullchain.pem",
		PrivateKey:            "/etc/probe-panel/tls/private-ca/privkey.pem",
		LetsEncryptLive:       "/etc/letsencrypt/live/probe-panel",
		LetsEncryptArchive:    "/etc/letsencrypt/archive/probe-panel",
		LetsEncryptRenewal:    "/etc/letsencrypt/renewal/probe-panel.conf",
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

type InstalledCommit func(time.Time) error

type HostPlatformValidator func(context.Context, Runner, string, string) error

type Config struct {
	BundleRoot           string
	ReleaseID            string
	PlatformID           string
	Paths                Paths
	Runner               Runner
	Bootstrapper         ApplicationBootstrapper
	IdentityLookup       IdentityLookup
	RootIdentity         Identity
	RequireRoot          bool
	Now                  func() time.Time
	ResolveHostname      func(context.Context, string) error
	CommitInstalled      InstalledCommit
	ValidateHostPlatform HostPlatformValidator
}

type Finalizer struct {
	config                         Config
	platform                       platformContract
	managementNginxStateCaptured   bool
	managementNginxWasActive       bool
	managementNginxCandidateStaged bool
	managementCertbotStateCaptured bool
	managementCertbotUnitFileState string
	managementCertbotWasActive     bool
}

// ErrPreflight marks a failure that occurred before the finalizer claimed or
// mutated any Probe production target. The root setup worker may safely return
// the state to configuring so the operator can correct the host/input and
// explicitly submit fresh secrets. Errors after ownership remain terminal.
var ErrPreflight = errors.New("setup production preflight failed")

func New(config Config) (*Finalizer, error) {
	platform, err := platformContractFor(config.PlatformID)
	if err != nil {
		return nil, err
	}
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
	if config.ValidateHostPlatform == nil {
		config.ValidateHostPlatform = validateHostPlatform
	}
	if config.BundleRoot == "" || !filepath.IsAbs(config.BundleRoot) || filepath.Clean(config.BundleRoot) != config.BundleRoot {
		return nil, errors.New("setup release bundle root must be an absolute canonical path")
	}
	if config.ReleaseID == "" || len(config.ReleaseID) > 96 || !safeReleaseID(config.ReleaseID) {
		return nil, errors.New("setup release identifier is invalid")
	}
	if config.CommitInstalled == nil {
		return nil, errors.New("setup installed-state commit is required")
	}
	if err := validatePaths(config.Paths); err != nil {
		return nil, err
	}
	finalizer := &Finalizer{config: config, platform: platform}
	if len(finalizer.nginxNativeUnitCandidates()) == 0 {
		return nil, errors.New("setup finalizer nginx unit path does not match the platform contract")
	}
	return finalizer, nil
}

func (finalizer *Finalizer) Finalize(ctx context.Context, request setup.CompleteRequest) (err error) {
	if finalizer == nil {
		return errors.New("setup production finalizer is not configured")
	}
	preflight := true
	defer func() {
		if err != nil && preflight && !errors.Is(err, ErrPreflight) {
			err = fmt.Errorf("%w: %v", ErrPreflight, err)
		}
	}()
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
	access, err := request.AccessConfiguration()
	if err != nil {
		return fmt.Errorf("resolve setup access configuration: %w", err)
	}
	if err := validateProductionRequest(request); err != nil {
		return err
	}
	if err := finalizer.validateBundle(access); err != nil {
		return err
	}
	if err := finalizer.config.ValidateHostPlatform(
		ctx, finalizer.config.Runner, finalizer.config.BundleRoot, finalizer.platform.id,
	); err != nil {
		return fmt.Errorf("validate management host platform: %w", err)
	}
	if err := finalizer.validateFreshIngressForAccess(access); err != nil {
		return err
	}
	preexistingNginxWants := false
	if access.Profile == setup.InstallationProfileManagement {
		preexistingNginxWants, err = finalizer.inspectNginxBootDependency()
		if err != nil {
			return fmt.Errorf("inspect existing Nginx enablement: %w", err)
		}
		nginxState, stateErr := finalizer.systemdProperty(ctx, "nginx.service", "ActiveState")
		if stateErr != nil {
			return fmt.Errorf("inspect existing Nginx activity: %w", stateErr)
		}
		switch nginxState {
		case "active":
			finalizer.managementNginxWasActive = true
		case "inactive", "failed":
			finalizer.managementNginxWasActive = false
		default:
			return fmt.Errorf("nginx.service has unstable active state %s", nginxState)
		}
		finalizer.managementNginxStateCaptured = true
		if err := finalizer.validateManagementNginxPreflight(ctx, access); err != nil {
			return err
		}
	}
	if access.Mode == setup.IngressModeIP {
		if err := finalizer.validateAvailableIPPortsForProfile(ctx, access.Profile); err != nil {
			return err
		}
	} else {
		// ACME port ownership is a first-install prerequisite, not a late
		// certificate error. Check before service-account creation, layout,
		// PostgreSQL startup, or any other persistent mutation.
		if err := finalizer.validateAvailableACMEPorts(ctx); err != nil {
			return err
		}
		if access.Profile == setup.InstallationProfileManagement {
			nginxState, stateErr := finalizer.systemdProperty(ctx, "nginx.service", "ActiveState")
			if stateErr != nil {
				return fmt.Errorf("inspect Nginx before management ACME setup: %w", stateErr)
			}
			if nginxState != "inactive" && nginxState != "failed" {
				return fmt.Errorf("management domain installation requires exclusive ACME ingress; nginx.service is %s", nginxState)
			}
		}
		for _, hostname := range configuredHostnames(request, access.Profile) {
			if err := finalizer.config.ResolveHostname(ctx, hostname); err != nil {
				return fmt.Errorf("resolve configured hostname %s: %w", hostname, err)
			}
		}
	}
	if err := finalizer.requireIdentityCapabilities("preflight"); err != nil {
		return err
	}
	if err := finalizer.validatePostgresServerVersion(ctx); err != nil {
		return err
	}
	if err := finalizer.validateDatabaseTargetsFresh(ctx, request); err != nil {
		return err
	}
	if access.Profile == setup.InstallationProfileManagement && access.Mode == setup.IngressModeDomain {
		if err := finalizer.captureManagementCertbotState(ctx); err != nil {
			return err
		}
	}
	identity, err := finalizer.config.IdentityLookup("probe-api")
	if err != nil {
		return errors.New("the bootstrap-created probe-api service account is unavailable")
	}
	if err := finalizer.validateLayoutPreflight(identity, access); err != nil {
		return err
	}

	// From this point this process owns the otherwise-clean Probe ingress. Any
	// failure keeps the public API and Nginx stopped while retaining database
	// and certificate material for an explicit recovery workflow.
	preflight = false
	ownedTarget := true
	defer func() {
		if err != nil && ownedTarget {
			if rollbackErr := finalizer.stopProductionForProfile(access.Profile, preexistingNginxWants); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("roll back production services: %w", rollbackErr))
			}
		}
	}()

	if err := finalizer.requireIdentityCapabilities("service identity lookup"); err != nil {
		return err
	}
	if err := finalizer.prepareLayout(identity, access); err != nil {
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
	databaseURL := postgresURL(request)
	if err := finalizer.writeConfiguration(request, access, databaseURL, identity); err != nil {
		return err
	}
	if err := finalizer.issueCertificate(ctx, request, access); err != nil {
		return err
	}
	if err := finalizer.validateIngressTLSWithStagedAPI(ctx, request, access); err != nil {
		return err
	}
	if access.Profile == setup.InstallationProfileManagement {
		if err := finalizer.validateManagementNginxCandidate(ctx); err != nil {
			return err
		}
	}
	if err := finalizer.configureCertificateTimer(ctx, access); err != nil {
		return err
	}
	if err := finalizer.createDatabase(ctx, request); err != nil {
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
	installArguments := []string{
		"--bundle-root", finalizer.config.BundleRoot,
		"--release-id", finalizer.config.ReleaseID,
	}
	if access.Profile == setup.InstallationProfileManagement {
		installArguments = append(installArguments, "--profile", string(access.Profile))
	} else {
		installArguments = append(installArguments, "--disable-default-site")
	}
	if err := finalizer.config.Runner.Run(ctx, installScript, installArguments...); err != nil {
		return fmt.Errorf("activate verified release: %w", err)
	}
	if err := finalizer.config.Runner.Run(ctx, "/usr/bin/systemctl", "disable", "probe-panel-setup.service", "probe-panel-setup.socket", "probe-panel-finalizer.path"); err != nil {
		return fmt.Errorf("disable first-run services: %w", err)
	}
	if err := finalizer.config.Runner.Run(ctx, "/usr/bin/systemctl", "stop", "probe-panel-finalizer.path"); err != nil {
		return fmt.Errorf("stop first-run finalizer trigger: %w", err)
	}
	// This root-owned persistent transition is the final fallible operation.
	// If it fails, the deferred rollback stops and disables every production
	// ingress so recovery_required can never coexist with a live formal panel.
	if err := finalizer.config.CommitInstalled(finalizer.config.Now().UTC()); err != nil {
		return fmt.Errorf("commit installed setup state: %w", err)
	}
	ownedTarget = false
	return nil
}

func (finalizer *Finalizer) configureCertificateTimer(ctx context.Context, access setup.AccessConfiguration) error {
	timerUnit := finalizer.platform.certbotTimerUnit
	if access.Mode == setup.IngressModeDomain {
		if access.Profile == setup.InstallationProfileManagement {
			if !finalizer.managementCertbotStateCaptured {
				return errors.New("management certificate timer state was not captured during preflight")
			}
			switch finalizer.managementCertbotUnitFileState {
			case "disabled":
				if err := finalizer.config.Runner.Run(ctx, "/usr/bin/systemctl", "enable", timerUnit); err != nil {
					return fmt.Errorf("enable certificate renewal timer: %w", err)
				}
				if err := finalizer.config.Runner.Run(ctx, "/usr/bin/systemctl", "start", timerUnit); err != nil {
					return fmt.Errorf("start certificate renewal timer: %w", err)
				}
			case "enabled", "enabled-runtime":
				// Preserve persistent versus runtime enablement exactly. Starting an
				// already enabled timer is sufficient and never promotes a runtime
				// relationship into a permanent one.
				if err := finalizer.config.Runner.Run(ctx, "/usr/bin/systemctl", "start", timerUnit); err != nil {
					return fmt.Errorf("start certificate renewal timer: %w", err)
				}
			default:
				return errors.New("management certificate timer enablement was not captured exactly")
			}
			return nil
		}
		if err := finalizer.config.Runner.Run(ctx, "/usr/bin/systemctl", "enable", timerUnit); err != nil {
			return fmt.Errorf("enable certificate renewal timer: %w", err)
		}
		if err := finalizer.config.Runner.Run(ctx, "/usr/bin/systemctl", "start", timerUnit); err != nil {
			return fmt.Errorf("start certificate renewal timer: %w", err)
		}
		return nil
	}
	if access.Profile == setup.InstallationProfileManagement {
		// The independent management IP profile neither uses nor owns certbot.
		// Preserve any timer installed for unrelated sites on a shared server.
		return nil
	}
	if err := finalizer.config.Runner.Run(ctx, "/usr/bin/systemctl", "stop", timerUnit); err != nil {
		return fmt.Errorf("stop unused certificate renewal timer: %w", err)
	}
	if err := finalizer.config.Runner.Run(ctx, "/usr/bin/systemctl", "disable", timerUnit); err != nil {
		return fmt.Errorf("disable unused certificate renewal timer: %w", err)
	}
	return nil
}

func (finalizer *Finalizer) captureManagementCertbotState(ctx context.Context) error {
	timerUnit := finalizer.platform.certbotTimerUnit
	unitFileState, err := finalizer.systemdProperty(ctx, timerUnit, "UnitFileState")
	if err != nil {
		return fmt.Errorf("inspect certificate timer enablement: %w", err)
	}
	switch unitFileState {
	case "disabled", "enabled", "enabled-runtime":
	default:
		return fmt.Errorf("certificate timer has unsupported unit state %s", unitFileState)
	}
	activeState, err := finalizer.systemdProperty(ctx, timerUnit, "ActiveState")
	if err != nil {
		return fmt.Errorf("inspect certificate timer activity: %w", err)
	}
	active := false
	switch activeState {
	case "active":
		active = true
	case "inactive", "failed":
	default:
		return fmt.Errorf("certificate timer has unstable active state %s", activeState)
	}
	finalizer.managementCertbotUnitFileState = unitFileState
	finalizer.managementCertbotWasActive = active
	finalizer.managementCertbotStateCaptured = true
	return nil
}

func (finalizer *Finalizer) validateIngressTLSWithStagedAPI(ctx context.Context, request setup.CompleteRequest, access setup.AccessConfiguration) error {
	binary := filepath.Join(finalizer.config.BundleRoot, "artifacts", "api", "probe-api")
	arguments := []string{"config", "validate-ingress-tls"}
	switch access.Mode {
	case setup.IngressModeDomain:
		if access.Profile == setup.InstallationProfileManagement {
			arguments = append(arguments, "admin-domain", request.Domains.Admin)
		} else {
			arguments = append(arguments, "domain", request.Domains.Panel, request.Domains.Admin, request.Domains.Agent)
		}
	case setup.IngressModeIP:
		mode := "ip"
		if access.Profile == setup.InstallationProfileManagement {
			mode = "admin-ip"
		}
		arguments = append(arguments, mode, access.Address.String())
	default:
		return errors.New("validate provisioned ingress TLS: unsupported ingress mode")
	}
	if err := finalizer.config.Runner.Run(ctx, binary, arguments...); err != nil {
		return fmt.Errorf("validate provisioned ingress TLS with staged API: %w", err)
	}
	return nil
}

func (finalizer *Finalizer) validateManagementNginxCandidate(ctx context.Context) (err error) {
	paths := finalizer.config.Paths
	if err := createAbsoluteSymlink(paths.ActiveNginxConfig, paths.NginxLink); err != nil {
		return fmt.Errorf("stage management Nginx candidate: %w", err)
	}
	finalizer.managementNginxCandidateStaged = true
	defer func() {
		removed, cleanupErr := finalizer.removeManagementNginxConfig()
		if cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove management Nginx candidate: %w", cleanupErr))
		} else if !removed {
			err = errors.Join(err, errors.New("remove management Nginx candidate: staged link disappeared"))
		}
		if (removed || cleanupErr == nil) && finalizer.managementNginxStateCaptured && finalizer.managementNginxWasActive {
			restoreContext, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if validateErr := finalizer.config.Runner.Run(restoreContext, "/usr/sbin/nginx", "-t"); validateErr != nil {
				err = errors.Join(err, fmt.Errorf("validate shared Nginx after removing management candidate: %w", validateErr))
			} else if reloadErr := finalizer.config.Runner.Run(restoreContext, "/usr/bin/systemctl", "reload", "nginx.service"); reloadErr != nil {
				err = errors.Join(err, fmt.Errorf("reload shared Nginx after removing management candidate: %w", reloadErr))
			}
		}
	}()
	if err := finalizer.config.Runner.Run(ctx, "/usr/sbin/nginx", "-t"); err != nil {
		return fmt.Errorf("validate management Nginx candidate with existing configuration: %w", err)
	}
	return nil
}

func (finalizer *Finalizer) validateManagementNginxPreflight(ctx context.Context, access setup.AccessConfiguration) error {
	output, err := finalizer.config.Runner.Output(ctx, "/usr/sbin/nginx", "-T")
	if err != nil {
		return fmt.Errorf("inspect existing Nginx configuration: %w", err)
	}
	defer clear(output)
	if len(output) == 0 || len(output) > 8*1024*1024 {
		return errors.New("existing Nginx configuration dump is empty or too large")
	}
	expectedInclude := filepath.Join(finalizer.config.Paths.NginxConfD, "*.conf")
	includeFound := false
	reservedPorts := map[string]struct{}{"18455": {}}
	if access.Mode == setup.IngressModeDomain {
		reservedPorts = map[string]struct{}{"80": {}, "443": {}}
	}
	for _, line := range strings.Split(string(output), "\n") {
		if comment := strings.IndexByte(line, '#'); comment >= 0 {
			line = line[:comment]
		}
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "include" && len(fields) == 2 && strings.TrimSuffix(fields[1], ";") == expectedInclude {
			includeFound = true
			continue
		}
		if fields[0] != "listen" || len(fields) < 2 {
			continue
		}
		port, parseErr := nginxListenPort(fields[1])
		if parseErr != nil {
			return fmt.Errorf("inspect existing Nginx listen directive: %w", parseErr)
		}
		if _, reserved := reservedPorts[port]; reserved {
			return fmt.Errorf("existing Nginx configuration already declares reserved management port %s", port)
		}
	}
	if !includeFound {
		return fmt.Errorf("existing Nginx configuration must include %s", expectedInclude)
	}
	return nil
}

func nginxListenPort(value string) (string, error) {
	value = strings.TrimSuffix(strings.TrimSpace(value), ";")
	if value == "" {
		return "", errors.New("empty listen address")
	}
	if strings.HasPrefix(value, "unix:") {
		return "", nil
	}
	if port, err := strconv.ParseUint(value, 10, 16); err == nil {
		if port == 0 || strconv.FormatUint(port, 10) != value {
			return "", errors.New("non-canonical listen port")
		}
		return value, nil
	}
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return "", errors.New("unsupported listen address")
	}
	parsed, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != port {
		return "", errors.New("non-canonical listen address port")
	}
	return port, nil
}

func (finalizer *Finalizer) validateBundle(access setup.AccessConfiguration) error {
	rootInfo, err := os.Lstat(finalizer.config.BundleRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("verified setup release bundle is unavailable")
	}
	required := []string{
		"BUNDLE-SHA256SUMS",
		"RELEASE-MANIFEST",
		"artifacts/api/probe-api",
		"source/probe-api/config/probe-api.env.example",
		"source/probe-api/config/probe-postgres-backup.env.example",
		"source/probe-api/deploy/scripts/deploy-common.sh",
		"source/probe-api/deploy/scripts/install-release.sh",
	}
	unitSources, ok := systemdUnitSourcesFor(finalizer.platform)
	if !ok {
		return errors.New("verified setup release has no reviewed systemd unit profile")
	}
	for _, relative := range unitSources.requiredBundlePaths() {
		required = append(required, "source/probe-api/"+relative)
	}
	if access.Profile == setup.InstallationProfileManagement {
		for _, templateName := range []string{
			"nginx-management.conf",
			"nginx-management-ip.conf",
			"nginx-management-legacy.conf",
			"nginx-management-ip-legacy.conf",
			"nginx-management-classic.conf",
			"nginx-management-ip-classic.conf",
		} {
			required = append(required, "source/probe-api/deploy/nginx/"+templateName)
		}
	} else {
		required = append(required,
			"source/probe-api/deploy/nginx/nginx.conf",
			"source/probe-api/deploy/nginx/nginx-ip.conf",
		)
	}
	for _, relative := range required {
		path := filepath.Join(finalizer.config.BundleRoot, filepath.FromSlash(relative))
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("verified setup release is incomplete: %s", relative)
		}
	}
	if err := finalizer.validateReleasePlatformManifest(); err != nil {
		return err
	}
	return nil
}

func validateHostPlatform(ctx context.Context, runner Runner, bundleRoot, platformID string) error {
	installScript := filepath.Join(bundleRoot, "source/probe-api/deploy/scripts/install-release.sh")
	if err := runner.Run(ctx, installScript, "--bundle-root", bundleRoot, "--check-platform", platformID); err != nil {
		return errors.New("the host or release bundle no longer matches the bootstrap platform contract")
	}
	return nil
}

func (finalizer *Finalizer) validateReleasePlatformManifest() error {
	manifestPath := filepath.Join(finalizer.config.BundleRoot, "RELEASE-MANIFEST")
	contents, err := readSmallRegular(manifestPath, 16*1024)
	if err != nil {
		return err
	}
	defer clear(contents)
	values := make(map[string]string)
	for _, line := range strings.Split(strings.ReplaceAll(string(contents), "\r\n", "\n"), "\n") {
		if line == "" {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || key == "" || value == "" || strings.TrimSpace(line) != line {
			return errors.New("release manifest contains an invalid platform contract")
		}
		if key != "runtime_abi" && key != "platform_ids" {
			continue
		}
		if _, duplicate := values[key]; duplicate {
			return errors.New("release manifest contains a duplicate platform contract")
		}
		values[key] = value
	}
	if values["runtime_abi"] != RuntimeABISystemdV2 {
		return errors.New("release manifest runtime ABI is unsupported")
	}
	if values["platform_ids"] != SupportedPlatformIDs {
		return errors.New("release manifest platform allowlist is unsupported")
	}
	return nil
}

func (finalizer *Finalizer) validateFreshIngress() error {
	return finalizer.validateFreshIngressForAccess(setup.AccessConfiguration{
		Mode: setup.IngressModeDomain, Profile: setup.InstallationProfileFull,
	})
}

func (finalizer *Finalizer) validateFreshIngressForAccess(access setup.AccessConfiguration) error {
	paths := finalizer.config.Paths
	managementProfile := access.Profile == setup.InstallationProfileManagement
	coexistingManagementIP := access.Profile == setup.InstallationProfileManagement && access.Mode == setup.IngressModeIP
	targets := []string{
		paths.APIPath, paths.AdminPath, paths.MigrationsPath,
		paths.APIEnvironment, paths.BackupEnvironment, paths.PGPass, paths.Allowlist,
		paths.ActiveNginxConfig, paths.NginxLink,
		paths.APIUnit, paths.BackupUnit, paths.BackupTimerUnit,
		filepath.Join(paths.ConfigDir, "probe-api.env.example"),
		filepath.Join(paths.ConfigDir, "probe-postgres-backup.env.example"),
		filepath.Join(paths.NginxConfigDir, "nginx-management.conf.example"),
		filepath.Join(paths.NginxConfigDir, "nginx-management-ip.conf.example"),
		filepath.Join(paths.BackupScriptDir, "backup-postgres.sh"),
		filepath.Join(paths.BackupScriptDir, "restore-postgres.sh"),
	}
	tlsSurfaces := []string{"panel", "admin", "api"}
	if managementProfile {
		tlsSurfaces = []string{"admin"}
	}
	for _, surface := range tlsSurfaces {
		targets = append(targets,
			filepath.Join(paths.TLSRoot, surface, "fullchain.pem"),
			filepath.Join(paths.TLSRoot, surface, "privkey.pem"),
		)
	}
	if access.Profile == setup.InstallationProfileManagement {
		if access.Mode == setup.IngressModeDomain {
			targets = append(targets,
				paths.LetsEncryptLive, paths.LetsEncryptArchive,
				paths.LetsEncryptRenewal, paths.LetsEncryptDeployHook,
			)
		} else {
			targets = append(targets,
				paths.PrivateTLSRoot, paths.PrivateCACertificate, paths.PrivateCAKey,
				paths.PrivateCertificate, paths.PrivateKey,
			)
		}
	} else {
		targets = append(targets, paths.AgentPath, paths.WebPath)
		// Preserve the historical full-profile rule: any TLS material from
		// either ingress mode makes a nominal first install non-fresh.
		targets = append(targets,
			paths.LetsEncryptLive, paths.LetsEncryptArchive, paths.LetsEncryptRenewal,
			paths.PrivateTLSRoot,
			paths.PrivateCACertificate, paths.PrivateCAKey,
			paths.PrivateCertificate, paths.PrivateKey,
			paths.LetsEncryptDeployHook,
		)
	}
	if !managementProfile {
		targets = append(targets, paths.NginxWantsLink)
	}
	for _, path := range targets {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("existing production asset prevents first installation: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect first-install target %s: %w", path, err)
		}
	}
	if !coexistingManagementIP {
		if err := validateNginxDirectory(paths.NginxConfD, ""); err != nil {
			return err
		}
		if err := validateNginxDirectory(paths.NginxSitesEnabled, "default"); err != nil {
			return err
		}
	}
	return nil
}

func (finalizer *Finalizer) layoutDirectories(identity Identity, access setup.AccessConfiguration) []directorySpec {
	root := finalizer.config.RootIdentity
	paths := finalizer.config.Paths
	directories := []directorySpec{
		{paths.ProbeRoot, 0o755, root},
		{filepath.Dir(paths.PostgresBackupDir), 0o710, Identity{UID: 0, GID: identity.GID}},
		// Nginx serves the public private-CA certificate through an alias below
		// /etc/probe-panel. Keep this parent traversable by its unprivileged
		// worker; the allowlist file and all private material retain their
		// narrower ownership and modes.
		{filepath.Dir(paths.Allowlist), 0o755, root},
		{paths.ConfigDir, 0o750, Identity{UID: 0, GID: identity.GID}},
		{paths.NginxConfigDir, 0o755, root},
		{filepath.Dir(paths.APIPath), 0o755, root},
		{paths.BackupScriptDir, 0o750, Identity{UID: 0, GID: identity.GID}},
		{paths.ReleaseDir, 0o755, root},
		{paths.BackupDir, 0o700, root},
		{paths.PostgresBackupDir, 0o700, identity},
		{paths.TLSRoot, 0o755, root},
		{filepath.Join(paths.TLSRoot, "admin"), 0o755, root},
	}
	if access.Profile != setup.InstallationProfileManagement {
		directories = append(directories,
			directorySpec{filepath.Join(paths.TLSRoot, "panel"), 0o755, root},
			directorySpec{filepath.Join(paths.TLSRoot, "api"), 0o755, root},
		)
	}
	if access.Mode == setup.IngressModeIP {
		// Nginx workers must be able to serve ca.pem and the probe-api service
		// must be able to read it when generating Agent install commands. The
		// private keys inside this directory remain root-only (0600).
		directories = append(directories, directorySpec{paths.PrivateTLSRoot, 0o755, root})
	}
	return directories
}

func (finalizer *Finalizer) sharedLayoutDirectories(access setup.AccessConfiguration) []directorySpec {
	if access.Mode != setup.IngressModeDomain {
		return nil
	}
	root := finalizer.config.RootIdentity
	paths := finalizer.config.Paths
	hook := paths.LetsEncryptDeployHook
	return []directorySpec{
		{filepath.Dir(filepath.Dir(filepath.Dir(hook))), 0o755, root},
		{filepath.Dir(paths.LetsEncryptLive), 0o755, root},
		{filepath.Dir(paths.LetsEncryptArchive), 0o755, root},
		{filepath.Dir(paths.LetsEncryptRenewal), 0o755, root},
		{filepath.Dir(filepath.Dir(hook)), 0o755, root},
		{filepath.Dir(hook), 0o755, root},
	}
}

func (finalizer *Finalizer) validateLayoutPreflight(identity Identity, access setup.AccessConfiguration) error {
	for _, directory := range finalizer.layoutDirectories(identity, access) {
		if _, err := validateExistingDirectory(directory, true); err != nil {
			return fmt.Errorf("validate existing managed layout: %w", err)
		}
	}
	for _, directory := range finalizer.sharedLayoutDirectories(access) {
		if _, err := validateExistingDirectory(directory, false); err != nil {
			return fmt.Errorf("validate existing shared layout: %w", err)
		}
	}
	return nil
}

func (finalizer *Finalizer) prepareLayout(identity Identity, access setup.AccessConfiguration) error {
	for _, directory := range finalizer.layoutDirectories(identity, access) {
		if err := ensureDirectory(directory); err != nil {
			return err
		}
	}
	for _, directory := range finalizer.sharedLayoutDirectories(access) {
		if err := ensureSharedRootDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func (finalizer *Finalizer) startLocalPostgres(ctx context.Context) error {
	// Bootstrap owns persistent enablement. Running enable here can delegate to
	// a distribution SysV compatibility helper. That helper is intentionally
	// outside this tightly sandboxed finalizer's writable paths.
	postgresServiceUnit := finalizer.platform.postgresServiceUnit
	if err := finalizer.config.Runner.Run(ctx, "/usr/bin/systemctl", "start", postgresServiceUnit); err != nil {
		return fmt.Errorf("start local PostgreSQL: %w", err)
	}
	if err := finalizer.config.Runner.RunQuiet(ctx, "/usr/bin/systemctl", "is-active", "--quiet", postgresServiceUnit); err != nil {
		return fmt.Errorf("verify local PostgreSQL service: %w", err)
	}
	var ready bool
	for attempt := 0; attempt < 20; attempt++ {
		if err := finalizer.config.Runner.RunQuiet(ctx, finalizer.platform.pgIsReadyPath, "--host", "127.0.0.1", "--port", "5432", "--timeout", "2"); err == nil {
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
	listeners, err := finalizer.config.Runner.Output(ctx, finalizer.platform.ssPath, "-H", "-lnt")
	if err != nil {
		return fmt.Errorf("inspect PostgreSQL listener: %w", err)
	}
	loopbackOnly, err := tcpPortRestrictedToLoopback(listeners, "5432")
	if err != nil {
		return fmt.Errorf("parse PostgreSQL listener inspection: %w", err)
	}
	if !loopbackOnly {
		return errors.New("PostgreSQL port 5432 is not restricted to 127.0.0.1 and ::1")
	}
	return nil
}

func (finalizer *Finalizer) createDatabase(ctx context.Context, request setup.CompleteRequest) error {
	if err := finalizer.validateDatabaseTargetsFresh(ctx, request); err != nil {
		return err
	}
	roleSQL := "SET standard_conforming_strings = on;\nCREATE ROLE " + quoteIdentifier(request.Database.Username) + " LOGIN PASSWORD " + quoteLiteral(request.Database.Password) + ";\n"
	roleBytes := []byte(roleSQL)
	defer clear(roleBytes)
	if err := finalizer.config.Runner.RunSensitive(ctx, roleBytes, "/usr/bin/env", finalizer.postgresCommand("--no-psqlrc", "--set=ON_ERROR_STOP=1", "--dbname=postgres", "--quiet")...); err != nil {
		return fmt.Errorf("create dedicated PostgreSQL role: %w", err)
	}
	databaseSQL := []byte("CREATE DATABASE " + quoteIdentifier(request.Database.Name) + " OWNER " + quoteIdentifier(request.Database.Username) + ";\n")
	defer clear(databaseSQL)
	if err := finalizer.config.Runner.RunSensitive(ctx, databaseSQL, "/usr/bin/env", finalizer.postgresCommand("--no-psqlrc", "--set=ON_ERROR_STOP=1", "--dbname=postgres", "--quiet")...); err != nil {
		return fmt.Errorf("create dedicated PostgreSQL database: %w", err)
	}
	return nil
}

func (finalizer *Finalizer) validateDatabaseTargetsFresh(ctx context.Context, request setup.CompleteRequest) error {
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
	return nil
}

func (finalizer *Finalizer) validatePostgresServerVersion(ctx context.Context) error {
	output, err := finalizer.config.Runner.Output(
		ctx, "/usr/bin/env",
		finalizer.postgresCommand(
			"--no-psqlrc", "--tuples-only", "--no-align", "--dbname=postgres",
			"--command", "SHOW server_version_num;",
		)...,
	)
	if err != nil {
		return errors.New("verify local PostgreSQL server version")
	}
	defer clear(output)
	value := strings.TrimSpace(string(output))
	if value == "" {
		return errors.New("local PostgreSQL returned an invalid server version number")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return errors.New("local PostgreSQL returned an invalid server version number")
		}
	}
	versionNumber, err := strconv.Atoi(value)
	if err != nil {
		return errors.New("local PostgreSQL returned an invalid server version number")
	}
	if versionNumber < minimumPostgresVersionNumber || versionNumber >= maximumPostgresVersionNumber {
		return fmt.Errorf("local PostgreSQL server version %d is unsupported; exact major version 14 is required", versionNumber)
	}
	return nil
}

func (finalizer *Finalizer) postgresObjectExists(ctx context.Context, table, column, value string) (bool, error) {
	query := "SELECT CASE WHEN EXISTS (SELECT 1 FROM " + table + " WHERE " + column + " = " + quoteLiteral([]byte(value)) + ") THEN 'yes' ELSE 'no' END;"
	output, err := finalizer.config.Runner.Output(ctx, "/usr/bin/env", finalizer.postgresCommand("--no-psqlrc", "--tuples-only", "--no-align", "--dbname=postgres", "--command", query)...)
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

func (finalizer *Finalizer) postgresCommand(arguments ...string) []string {
	prefix := []string{
		"-i",
		"HOME=/var/lib/postgresql",
		"USER=postgres",
		"LOGNAME=postgres",
		"SHELL=/bin/sh",
		"PATH=" + postgresCommandPath,
		"/usr/bin/setpriv",
		"--reuid=postgres", "--regid=postgres", "--init-groups",
		"--", "/usr/bin/setpriv",
		"--inh-caps=-all",
	}
	if finalizer.platform.setprivSupportsAmbientCaps {
		prefix = append(prefix, "--ambient-caps=-all")
	}
	prefix = append(prefix, "--", finalizer.platform.psqlPath)
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

func (finalizer *Finalizer) writeConfiguration(request setup.CompleteRequest, access setup.AccessConfiguration, databaseURL string, identity Identity) error {
	root := finalizer.config.RootIdentity
	apiExample, err := readSmallRegular(filepath.Join(finalizer.config.BundleRoot, "source/probe-api/config/probe-api.env.example"), 128*1024)
	if err != nil {
		return err
	}
	apiValues := map[string]string{
		"PROBE_DATABASE_URL":         databaseURL,
		"PROBE_PLATFORM_ID":          finalizer.platform.id,
		"PROBE_INSTALLATION_PROFILE": string(access.Profile),
		"PROBE_INGRESS_MODE":         string(access.Mode),
		"PROBE_ADMIN_ORIGIN":         access.AdminOrigin,
		"PROBE_ADMIN_ALLOWLIST_FILE": finalizer.config.Paths.Allowlist,
		"PROBE_TRUSTED_PROXY_CIDRS":  "127.0.0.1/32,::1/128",
		"PROBE_API_LISTEN_ADDR":      "127.0.0.1:8080",
	}
	allowedEmptyAPIValues := map[string]struct{}{}
	if access.Profile == setup.InstallationProfileManagement {
		apiValues["PROBE_AGENT_PUBLIC_URL"] = ""
		apiValues["PROBE_AGENT_INSTALLER_URL"] = ""
		apiValues["PROBE_AGENT_INSTALL_CA_FILE"] = ""
		allowedEmptyAPIValues["PROBE_AGENT_PUBLIC_URL"] = struct{}{}
		allowedEmptyAPIValues["PROBE_AGENT_INSTALLER_URL"] = struct{}{}
		allowedEmptyAPIValues["PROBE_AGENT_INSTALL_CA_FILE"] = struct{}{}
	} else {
		apiValues["PROBE_AGENT_PUBLIC_URL"] = access.AgentOrigin
		apiValues["PROBE_AGENT_INSTALLER_URL"] = verifiedAgentInstallerURL
	}
	if access.Profile != setup.InstallationProfileManagement && access.Mode == setup.IngressModeIP {
		apiValues["PROBE_AGENT_INSTALL_CA_FILE"] = finalizer.config.Paths.PrivateCACertificate
	}
	apiEnvironment, err := replaceEnvironmentAllowingEmpty(apiExample, apiValues, allowedEmptyAPIValues)
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
	nginxTemplateName := nginxTemplateFor(access, finalizer.platform)
	nginxTemplate, err := readSmallRegular(filepath.Join(finalizer.config.BundleRoot, "source/probe-api/deploy/nginx", nginxTemplateName), 1024*1024)
	if err != nil {
		return err
	}
	var nginxConfig []byte
	if access.Mode == setup.IngressModeIP {
		nginxConfig, err = renderIPNginx(nginxTemplate, access)
	} else if access.Profile == setup.InstallationProfileManagement {
		nginxConfig, err = renderManagementNginx(nginxTemplate, request.Domains.Admin)
	} else {
		nginxConfig, err = renderNginx(nginxTemplate, request.Domains)
	}
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

func (finalizer *Finalizer) issueCertificate(ctx context.Context, request setup.CompleteRequest, access setup.AccessConfiguration) error {
	if access.Mode == setup.IngressModeIP {
		return finalizer.issuePrivateCertificate(access)
	}
	if err := finalizer.config.Runner.Run(ctx, "/usr/bin/systemctl", "stop", "nginx.service"); err != nil {
		return fmt.Errorf("stop Nginx before ACME challenge: %w", err)
	}
	if err := finalizer.validateAvailableACMEPorts(ctx); err != nil {
		return err
	}
	certbotArguments := []string{
		"certonly", "--standalone", "--non-interactive", "--agree-tos",
		"--email", request.TLS.Email, "--cert-name", "probe-panel",
		"--preferred-challenges", "http",
	}
	certbotArguments = append(certbotArguments, certbotDomainArguments(request, access.Profile)...)
	if err := finalizer.config.Runner.Run(ctx, "/usr/bin/certbot", certbotArguments...); err != nil {
		return fmt.Errorf("obtain ACME certificate: %w", err)
	}
	certificatePath := filepath.Join(finalizer.config.Paths.LetsEncryptLive, "fullchain.pem")
	privateKeyPath := filepath.Join(finalizer.config.Paths.LetsEncryptLive, "privkey.pem")
	if err := validateCertificateForProfile(certificatePath, privateKeyPath, request.Domains, access.Profile, finalizer.config.Now()); err != nil {
		return err
	}
	surfaces := []string{"panel", "admin", "api"}
	if access.Profile == setup.InstallationProfileManagement {
		surfaces = []string{"admin"}
	}
	for _, surface := range surfaces {
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

func (finalizer *Finalizer) stopProduction() error {
	return finalizer.stopProductionForProfile(setup.InstallationProfileFull, false)
}

func (finalizer *Finalizer) stopProductionForProfile(profile setup.InstallationProfile, preexistingNginxWants bool) error {
	if profile == setup.InstallationProfileManagement {
		return finalizer.stopManagementProduction(preexistingNginxWants)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var failures []error
	record := func(operation string, err error) {
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", operation, err))
		}
	}
	runtimeUnits := []string{
		"probe-api.service",
		"probe-postgres-backup.timer",
		"nginx.service",
		finalizer.platform.certbotTimerUnit,
	}
	// Attempt every stop independently. A failure involving one unit must not
	// prevent Nginx or the formal API from receiving their own stop request.
	for _, unit := range runtimeUnits {
		record("stop "+unit, finalizer.config.Runner.RunQuiet(ctx, "/usr/bin/systemctl", "stop", unit))
	}
	for _, unit := range []string{"probe-api.service", "probe-postgres-backup.timer", finalizer.platform.certbotTimerUnit} {
		record("disable "+unit, finalizer.config.Runner.RunQuiet(ctx, "/usr/bin/systemctl", "disable", unit))
	}
	// A distribution nginx unit can invoke a SysV helper when disabled. Probe
	// activation deliberately creates one reviewed add-wants symlink, so remove
	// only that exact relationship without crossing into /etc/rc*.d.
	record("remove nginx boot dependency", finalizer.removeNginxBootDependency(ctx))

	for _, unit := range runtimeUnits {
		activeState, err := finalizer.systemdProperty(ctx, unit, "ActiveState")
		if err != nil {
			record("verify "+unit+" runtime state", err)
			continue
		}
		if activeState != "inactive" && activeState != "failed" {
			failures = append(failures, fmt.Errorf("%s remains %s", unit, activeState))
		}
	}
	nginxUnitState, err := finalizer.systemdProperty(ctx, "nginx.service", "UnitFileState")
	if err != nil {
		record("verify nginx.service enablement", err)
	} else {
		switch nginxUnitState {
		case "disabled", "static", "masked", "masked-runtime":
		default:
			failures = append(failures, fmt.Errorf("nginx.service remains enabled with unit state %s", nginxUnitState))
		}
	}
	return errors.Join(failures...)
}

func (finalizer *Finalizer) stopManagementProduction(preexistingNginxWants bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var failures []error
	record := func(operation string, err error) {
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", operation, err))
		}
	}
	for _, unit := range []string{"probe-api.service", "probe-postgres-backup.timer"} {
		record("stop "+unit, finalizer.config.Runner.RunQuiet(ctx, "/usr/bin/systemctl", "stop", unit))
		record("disable "+unit, finalizer.config.Runner.RunQuiet(ctx, "/usr/bin/systemctl", "disable", unit))
	}
	if finalizer.managementCertbotStateCaptured {
		record("restore certificate timer state", finalizer.restoreManagementCertbotState(ctx))
	}

	removedNginxConfig, err := finalizer.removeManagementNginxConfig()
	record("remove management Nginx configuration", err)
	if removedNginxConfig || (finalizer.managementNginxCandidateStaged && err == nil) {
		if validateErr := finalizer.config.Runner.Run(ctx, "/usr/sbin/nginx", "-t"); validateErr != nil {
			record("validate shared Nginx after management rollback", validateErr)
		} else if finalizer.managementNginxStateCaptured && finalizer.managementNginxWasActive {
			record("reload shared Nginx after management rollback", finalizer.config.Runner.Run(ctx, "/usr/bin/systemctl", "reload", "nginx.service"))
		}
	}
	if finalizer.managementNginxStateCaptured && !finalizer.managementNginxWasActive {
		record("restore Nginx inactive state", finalizer.config.Runner.RunQuiet(ctx, "/usr/bin/systemctl", "stop", "nginx.service"))
	}
	if !preexistingNginxWants {
		record("remove management-created nginx boot dependency", finalizer.removeNginxBootDependency(ctx))
	}

	for _, unit := range []string{"probe-api.service", "probe-postgres-backup.timer"} {
		activeState, stateErr := finalizer.systemdProperty(ctx, unit, "ActiveState")
		if stateErr != nil {
			record("verify "+unit+" runtime state", stateErr)
			continue
		}
		if activeState != "inactive" && activeState != "failed" {
			failures = append(failures, fmt.Errorf("%s remains %s", unit, activeState))
		}
	}
	return errors.Join(failures...)
}

func (finalizer *Finalizer) restoreManagementCertbotState(ctx context.Context) error {
	timerUnit := finalizer.platform.certbotTimerUnit
	var failures []error
	record := func(operation string, err error) {
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", operation, err))
		}
	}
	switch finalizer.managementCertbotUnitFileState {
	case "disabled":
		record("restore disabled enablement", finalizer.config.Runner.RunQuiet(ctx, "/usr/bin/systemctl", "disable", timerUnit))
	case "enabled":
		record("restore persistent enablement", finalizer.config.Runner.RunQuiet(ctx, "/usr/bin/systemctl", "enable", timerUnit))
	case "enabled-runtime":
		// Remove any persistent relationship before recreating only the
		// original volatile /run/systemd relationship.
		record("remove persistent enablement", finalizer.config.Runner.RunQuiet(ctx, "/usr/bin/systemctl", "disable", timerUnit))
		record("restore runtime enablement", finalizer.config.Runner.RunQuiet(ctx, "/usr/bin/systemctl", "enable", "--runtime", timerUnit))
	default:
		failures = append(failures, errors.New("certificate timer original enablement is unavailable"))
	}
	if finalizer.managementCertbotWasActive {
		record("restore active state", finalizer.config.Runner.RunQuiet(ctx, "/usr/bin/systemctl", "start", timerUnit))
	} else {
		record("restore inactive state", finalizer.config.Runner.RunQuiet(ctx, "/usr/bin/systemctl", "stop", timerUnit))
	}
	return errors.Join(failures...)
}

func (finalizer *Finalizer) removeManagementNginxConfig() (bool, error) {
	link := finalizer.config.Paths.NginxLink
	info, err := os.Lstat(link)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect management Nginx link: %w", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return false, errors.New("refuse to remove management Nginx path because it is not a symlink")
	}
	resolvedLink, err := filepath.EvalSymlinks(link)
	if err != nil {
		return false, fmt.Errorf("resolve management Nginx link: %w", err)
	}
	resolvedConfig, err := filepath.EvalSymlinks(finalizer.config.Paths.ActiveNginxConfig)
	if err != nil {
		return false, fmt.Errorf("resolve generated management Nginx configuration: %w", err)
	}
	if filepath.Clean(resolvedLink) != filepath.Clean(resolvedConfig) {
		return false, errors.New("refuse to remove management Nginx link with an unexpected target")
	}
	if err := os.Remove(link); err != nil {
		return false, fmt.Errorf("remove management Nginx link: %w", err)
	}
	if err := syncDirectory(filepath.Dir(link)); err != nil {
		return true, fmt.Errorf("sync management Nginx configuration directory: %w", err)
	}
	return true, nil
}

func (finalizer *Finalizer) inspectNginxBootDependency() (bool, error) {
	paths := finalizer.config.Paths
	linkInfo, err := os.Lstat(paths.NginxWantsLink)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect nginx multi-user wants link: %w", err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		return false, errors.New("existing nginx multi-user wants path is not a symlink")
	}
	if err := rejectSymlinkComponents(filepath.Dir(paths.NginxWantsLink)); err != nil {
		return false, fmt.Errorf("validate nginx multi-user wants parent: %w", err)
	}
	resolvedLink, err := filepath.EvalSymlinks(paths.NginxWantsLink)
	if err != nil {
		return false, fmt.Errorf("resolve nginx multi-user wants link: %w", err)
	}
	if _, err := finalizer.matchNginxNativeUnit(resolvedLink, true); err != nil {
		return false, fmt.Errorf("validate nginx multi-user wants target: %w", err)
	}
	return true, nil
}

func (finalizer *Finalizer) removeNginxBootDependency(ctx context.Context) error {
	paths := finalizer.config.Paths
	linkInfo, err := os.Lstat(paths.NginxWantsLink)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect nginx multi-user wants link: %w", err)
		}
		// Reload even when the relationship is already absent. This keeps a
		// repeated rollback idempotent while making systemd forget stale unit
		// enablement state before stopProduction verifies UnitFileState.
		if err := finalizer.config.Runner.RunQuiet(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
			return fmt.Errorf("reload systemd after confirming nginx boot dependency is absent: %w", err)
		}
		return nil
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		return errors.New("refuse to remove nginx multi-user wants path because it is not a symlink")
	}
	resolvedLink, err := filepath.EvalSymlinks(paths.NginxWantsLink)
	if err != nil {
		return fmt.Errorf("resolve nginx multi-user wants link: %w", err)
	}
	if _, err := finalizer.matchNginxNativeUnit(resolvedLink, false); err != nil {
		return fmt.Errorf("refuse to remove nginx multi-user wants link: %w", err)
	}
	if err := os.Remove(paths.NginxWantsLink); err != nil {
		return fmt.Errorf("remove reviewed nginx multi-user wants link: %w", err)
	}
	if _, err := os.Lstat(paths.NginxWantsLink); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return errors.New("nginx multi-user wants link still exists after removal")
		}
		return fmt.Errorf("verify nginx multi-user wants link removal: %w", err)
	}
	if err := finalizer.config.Runner.RunQuiet(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd after removing nginx boot dependency: %w", err)
	}
	return nil
}

func (finalizer *Finalizer) matchNginxNativeUnit(resolvedTarget string, rejectLeafSymlink bool) (string, error) {
	for _, candidate := range finalizer.nginxNativeUnitCandidates() {
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inspect reviewed nginx systemd unit %s: %w", candidate, err)
		}
		if rejectLeafSymlink {
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
		} else {
			resolvedInfo, statErr := os.Stat(candidate)
			if statErr != nil || !resolvedInfo.Mode().IsRegular() {
				continue
			}
		}
		resolvedCandidate, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", fmt.Errorf("resolve reviewed nginx systemd unit %s: %w", candidate, err)
		}
		if filepath.Clean(resolvedTarget) == filepath.Clean(resolvedCandidate) {
			return candidate, nil
		}
	}
	return "", errors.New("target is not a reviewed native nginx systemd unit")
}

func (finalizer *Finalizer) nginxNativeUnitCandidates() []string {
	configuredPrimary := filepath.Clean(finalizer.config.Paths.NginxNativeUnit)
	reviewedPrimary := finalizer.platform.nginxNativeUnitPaths[0]
	if reviewedPrimary == "" {
		return nil
	}

	root := ""
	reviewedPrimaryPath := filepath.Clean(filepath.FromSlash(reviewedPrimary))
	if configuredPrimary != reviewedPrimaryPath {
		relativePrimary := filepath.FromSlash(strings.TrimPrefix(reviewedPrimary, "/"))
		suffix := string(filepath.Separator) + relativePrimary
		if !strings.HasSuffix(configuredPrimary, suffix) {
			return nil
		}
		root = strings.TrimSuffix(configuredPrimary, suffix)
	}

	candidates := make([]string, 0, len(finalizer.platform.nginxNativeUnitPaths))
	for _, reviewed := range finalizer.platform.nginxNativeUnitPaths {
		if reviewed == "" {
			continue
		}
		candidate := filepath.Clean(filepath.FromSlash(reviewed))
		if root != "" {
			candidate = filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(reviewed, "/")))
		}
		if len(candidates) == 0 || candidate != candidates[len(candidates)-1] {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func (finalizer *Finalizer) systemdProperty(ctx context.Context, unit, property string) (string, error) {
	if unit == "" || property == "" || strings.ContainsAny(unit, "\r\n") || strings.ContainsAny(property, "=\r\n") {
		return "", errors.New("systemd property request is invalid")
	}
	output, err := finalizer.config.Runner.Output(ctx, "/usr/bin/systemctl", "show", "--property="+property, unit)
	if err != nil {
		return "", err
	}
	defer clear(output)
	line := string(output)
	if strings.HasSuffix(line, "\n") {
		line = strings.TrimSuffix(line, "\n")
	}
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return "", errors.New("systemd returned an invalid property value")
	}
	prefix := property + "="
	if !strings.HasPrefix(line, prefix) {
		return "", errors.New("systemd returned an unexpected property record")
	}
	value := strings.TrimPrefix(line, prefix)
	if value == "" || strings.TrimSpace(value) != value {
		return "", errors.New("systemd returned an invalid property value")
	}
	return value, nil
}

func validateProductionRequest(request setup.CompleteRequest) error {
	if request.Database.Name == "postgres" || request.Database.Name == "template0" || request.Database.Name == "template1" || request.Database.Username == "postgres" {
		return errors.New("reserved PostgreSQL role or database name is not allowed")
	}
	access, err := request.AccessConfiguration()
	if err != nil {
		return err
	}
	if access.Mode == setup.IngressModeDomain {
		domains := configuredHostnames(request, access.Profile)
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
	}
	for _, character := range request.Database.Password {
		if character < 0x20 || character == 0x7f {
			return errors.New("database password must not contain control characters")
		}
	}
	return nil
}

func configuredHostnames(request setup.CompleteRequest, profile setup.InstallationProfile) []string {
	if profile == setup.InstallationProfileManagement {
		return []string{request.Domains.Admin}
	}
	return []string{request.Domains.Panel, request.Domains.Admin, request.Domains.Agent}
}

func nginxTemplateFor(access setup.AccessConfiguration, platform platformContract) string {
	if access.Profile == setup.InstallationProfileManagement {
		switch platform.nginxDialect {
		case nginxDialectClassic:
			if access.Mode == setup.IngressModeIP {
				return "nginx-management-ip-classic.conf"
			}
			return "nginx-management-classic.conf"
		case nginxDialectLegacy:
			if access.Mode == setup.IngressModeIP {
				return "nginx-management-ip-legacy.conf"
			}
			return "nginx-management-legacy.conf"
		case nginxDialectModern:
			if access.Mode == setup.IngressModeIP {
				return "nginx-management-ip.conf"
			}
			return "nginx-management.conf"
		default:
			return ""
		}
	}
	if access.Mode == setup.IngressModeIP {
		return "nginx-ip.conf"
	}
	return "nginx.conf"
}

func managementNginxTemplateNames(platform platformContract) []string {
	switch platform.nginxDialect {
	case nginxDialectClassic:
		return []string{"nginx-management-classic.conf", "nginx-management-ip-classic.conf"}
	case nginxDialectLegacy:
		return []string{"nginx-management-legacy.conf", "nginx-management-ip-legacy.conf"}
	case nginxDialectModern:
		return []string{"nginx-management.conf", "nginx-management-ip.conf"}
	default:
		return nil
	}
}

type systemdUnitSources struct {
	setupService        string
	setupSocket         string
	managementFinalizer string
	apiService          string
	backupService       string
	backupTimer         string
}

func systemdUnitSourcesFor(platform platformContract) (systemdUnitSources, bool) {
	switch platform.unitProfile {
	case SystemdUnitProfileLegacy:
		return systemdUnitSources{
			setupService:        "deploy/setup/probe-panel-setup-legacy.service",
			setupSocket:         "deploy/setup/probe-panel-setup-legacy.socket",
			managementFinalizer: "deploy/setup/probe-panel-finalizer-management-legacy.service",
			apiService:          "deploy/systemd/probe-api-legacy.service",
			backupService:       "deploy/systemd/probe-postgres-backup-legacy.service",
			backupTimer:         "deploy/systemd/probe-postgres-backup-legacy.timer",
		}, true
	case SystemdUnitProfileModern:
		return systemdUnitSources{
			setupService:        "deploy/setup/probe-panel-setup.service",
			setupSocket:         "deploy/setup/probe-panel-setup.socket",
			managementFinalizer: "deploy/setup/probe-panel-finalizer-management.service",
			apiService:          "deploy/systemd/probe-api.service",
			backupService:       "deploy/systemd/probe-postgres-backup.service",
			backupTimer:         "deploy/systemd/probe-postgres-backup.timer",
		}, true
	default:
		return systemdUnitSources{}, false
	}
}

func (sources systemdUnitSources) requiredBundlePaths() []string {
	return []string{
		sources.setupService,
		sources.setupSocket,
		"deploy/setup/probe-panel-finalizer.path",
		sources.managementFinalizer,
		sources.apiService,
		sources.backupService,
		sources.backupTimer,
	}
}

func certbotDomainArguments(request setup.CompleteRequest, profile setup.InstallationProfile) []string {
	arguments := make([]string, 0, 6)
	for _, hostname := range configuredHostnames(request, profile) {
		arguments = append(arguments, "--domain", hostname)
	}
	return arguments
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

func renderManagementNginx(template []byte, adminDomain string) ([]byte, error) {
	contents := string(template)
	const placeholder = "admin.example.com"
	if !strings.Contains(contents, placeholder) {
		return nil, fmt.Errorf("management Nginx template is missing %s", placeholder)
	}
	contents = strings.ReplaceAll(contents, placeholder, adminDomain)
	if strings.Contains(contents, ".example.com") {
		return nil, errors.New("management Nginx template still contains an example hostname")
	}
	return []byte(contents), nil
}

func renderIPNginx(template []byte, access setup.AccessConfiguration) ([]byte, error) {
	if access.Mode != setup.IngressModeIP || !access.Address.IsValid() {
		return nil, errors.New("IP Nginx rendering requires canonical IP access configuration")
	}
	contents := string(template)
	const placeholder = "PROBE_SETUP_SERVER_IP"
	ports := []int{setup.PanelHTTPSPort, setup.AgentHTTPSPort, setup.AdminHTTPSPort}
	if access.Profile == setup.InstallationProfileManagement {
		ports = []int{setup.AdminHTTPSPort}
	}
	for _, port := range ports {
		required := placeholder + ":" + strconv.Itoa(port)
		if !strings.Contains(contents, required) {
			return nil, fmt.Errorf("IP Nginx template is missing %s", required)
		}
	}
	address := access.Address.String()
	if access.Address.Is6() {
		address = "[" + address + "]"
	}
	contents = strings.ReplaceAll(contents, placeholder, address)
	if strings.Contains(contents, placeholder) {
		return nil, errors.New("IP Nginx template still contains its placeholder address")
	}
	return []byte(contents), nil
}

func replaceEnvironment(example []byte, replacements map[string]string) ([]byte, error) {
	return replaceEnvironmentAllowingEmpty(example, replacements, nil)
}

func replaceEnvironmentAllowingEmpty(example []byte, replacements map[string]string, allowedEmpty map[string]struct{}) ([]byte, error) {
	for key, value := range replacements {
		_, emptyAllowed := allowedEmpty[key]
		if (value == "" && !emptyAllowed) || strings.ContainsAny(value, " \t\r\n") {
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
	return validateCertificateForProfile(certificatePath, privateKeyPath, domains, setup.InstallationProfileFull, now)
}

func validateCertificateForProfile(certificatePath, privateKeyPath string, domains setup.DomainInput, profile setup.InstallationProfile, now time.Time) error {
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
	hostnames := []string{domains.Panel, domains.Admin, domains.Agent}
	if profile == setup.InstallationProfileManagement {
		hostnames = []string{domains.Admin}
	}
	for _, hostname := range hostnames {
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

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func validatePaths(paths Paths) error {
	for _, path := range []string{
		paths.ProbeRoot, paths.APIPath, paths.AgentPath, paths.WebPath, paths.AdminPath,
		paths.MigrationsPath, paths.ConfigDir, paths.NginxConfigDir, paths.BackupScriptDir,
		paths.ReleaseDir, paths.BackupDir, paths.PostgresBackupDir, paths.APIEnvironment,
		paths.BackupEnvironment, paths.PGPass, paths.Allowlist, paths.ActiveNginxConfig,
		paths.NginxLink, paths.NginxConfD, paths.NginxSitesEnabled, paths.NginxWantsLink,
		paths.NginxNativeUnit, paths.TLSRoot,
		paths.PrivateTLSRoot, paths.PrivateCACertificate, paths.PrivateCAKey,
		paths.PrivateCertificate, paths.PrivateKey,
		paths.LetsEncryptLive, paths.LetsEncryptArchive, paths.LetsEncryptRenewal,
		paths.LetsEncryptDeployHook, paths.APIUnit,
		paths.BackupUnit, paths.BackupTimerUnit,
	} {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("setup finalizer paths must be absolute and canonical")
		}
	}
	if filepath.Base(paths.NginxWantsLink) != "nginx.service" ||
		filepath.Base(filepath.Dir(paths.NginxWantsLink)) != "multi-user.target.wants" ||
		filepath.Base(paths.NginxNativeUnit) != "nginx.service" ||
		filepath.Base(filepath.Dir(paths.NginxNativeUnit)) != "system" ||
		filepath.Base(filepath.Dir(filepath.Dir(paths.NginxNativeUnit))) != "systemd" {
		return errors.New("setup finalizer nginx systemd paths are outside the reviewed unit relationship")
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
