package config

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"probe-api/internal/access"
)

const defaultAgentInstallerURL = "https://raw.githubusercontent.com/Kcmose/my-agent/refs/tags/v1.0.0/deploy/install.sh"

var agentInstallerVersionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type Config struct {
	ListenAddress             string
	DatabaseURL               string
	LogLevel                  string
	ReadHeaderTimeout         time.Duration
	ReadTimeout               time.Duration
	WriteTimeout              time.Duration
	IdleTimeout               time.Duration
	ShutdownTimeout           time.Duration
	DatabasePingTimeout       time.Duration
	DatabaseMaxConns          int32
	DatabaseMinConns          int32
	MaxAgentBodyBytes         int64
	MaxPanelBodyBytes         int64
	AdminOrigin               string
	AgentPublicURL            string
	AgentInstallerURL         string
	AgentInstallCAFile        string
	AgentInstallCAPEM         []byte
	AdminAllowlistFile        string
	AdminAllowlist            access.CIDRSet
	TrustedProxies            access.CIDRSet
	SessionTTL                time.Duration
	SessionMaxPerUser         int32
	SessionRetention          time.Duration
	LoginIPLimit              int32
	LoginIPWindow             time.Duration
	LoginUsernameLimit        int32
	LoginUsernameWindow       time.Duration
	AgentEnrollIPLimit        int32
	AgentRuntimeIPLimit       int32
	AgentNodeLimit            int32
	AgentRateWindow           time.Duration
	RateLimitMaxKeysPerBucket int32
	NodeOfflineAfter          time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		ListenAddress:             envString("PROBE_API_LISTEN_ADDR", "127.0.0.1:8080"),
		DatabaseURL:               strings.TrimSpace(os.Getenv("PROBE_DATABASE_URL")),
		LogLevel:                  strings.ToLower(envString("PROBE_LOG_LEVEL", "info")),
		ReadHeaderTimeout:         5 * time.Second,
		ReadTimeout:               15 * time.Second,
		WriteTimeout:              15 * time.Second,
		IdleTimeout:               60 * time.Second,
		ShutdownTimeout:           15 * time.Second,
		DatabasePingTimeout:       5 * time.Second,
		DatabaseMaxConns:          20,
		DatabaseMinConns:          2,
		MaxAgentBodyBytes:         256 * 1024,
		MaxPanelBodyBytes:         64 * 1024,
		AdminOrigin:               envString("PROBE_ADMIN_ORIGIN", "https://admin.example.com"),
		AgentPublicURL:            envString("PROBE_AGENT_PUBLIC_URL", "https://api.example.com"),
		AgentInstallerURL:         envString("PROBE_AGENT_INSTALLER_URL", defaultAgentInstallerURL),
		AgentInstallCAFile:        strings.TrimSpace(os.Getenv("PROBE_AGENT_INSTALL_CA_FILE")),
		AdminAllowlistFile:        strings.TrimSpace(os.Getenv("PROBE_ADMIN_ALLOWLIST_FILE")),
		SessionTTL:                12 * time.Hour,
		SessionMaxPerUser:         5,
		SessionRetention:          24 * time.Hour,
		LoginIPLimit:              10,
		LoginIPWindow:             time.Minute,
		LoginUsernameLimit:        5,
		LoginUsernameWindow:       5 * time.Minute,
		AgentEnrollIPLimit:        20,
		AgentRuntimeIPLimit:       600,
		AgentNodeLimit:            120,
		AgentRateWindow:           time.Minute,
		RateLimitMaxKeysPerBucket: 10000,
		NodeOfflineAfter:          45 * time.Second,
	}
	var err error
	trustedProxyValue := envString("PROBE_TRUSTED_PROXY_CIDRS", "127.0.0.1/32,::1/128")
	cfg.TrustedProxies, err = access.ParseCIDRList(trustedProxyValue)
	if err != nil {
		return Config{}, fmt.Errorf("PROBE_TRUSTED_PROXY_CIDRS: %w", err)
	}
	if cfg.AdminAllowlistFile != "" {
		cfg.AdminAllowlist, err = access.LoadNginxGeoAllowlist(cfg.AdminAllowlistFile)
		if err != nil {
			return Config{}, fmt.Errorf("PROBE_ADMIN_ALLOWLIST_FILE: %w", err)
		}
	}

	if cfg.ReadHeaderTimeout, err = envDuration("PROBE_API_READ_HEADER_TIMEOUT", cfg.ReadHeaderTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ReadTimeout, err = envDuration("PROBE_API_READ_TIMEOUT", cfg.ReadTimeout); err != nil {
		return Config{}, err
	}
	if cfg.WriteTimeout, err = envDuration("PROBE_API_WRITE_TIMEOUT", cfg.WriteTimeout); err != nil {
		return Config{}, err
	}
	if cfg.IdleTimeout, err = envDuration("PROBE_API_IDLE_TIMEOUT", cfg.IdleTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = envDuration("PROBE_API_SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout); err != nil {
		return Config{}, err
	}
	if cfg.DatabasePingTimeout, err = envDuration("PROBE_DATABASE_PING_TIMEOUT", cfg.DatabasePingTimeout); err != nil {
		return Config{}, err
	}
	if cfg.DatabaseMaxConns, err = envInt32("PROBE_DATABASE_MAX_CONNS", cfg.DatabaseMaxConns); err != nil {
		return Config{}, err
	}
	if cfg.DatabaseMinConns, err = envInt32("PROBE_DATABASE_MIN_CONNS", cfg.DatabaseMinConns); err != nil {
		return Config{}, err
	}
	if cfg.SessionTTL, err = envDuration("PROBE_SESSION_TTL", cfg.SessionTTL); err != nil {
		return Config{}, err
	}
	if cfg.SessionMaxPerUser, err = envInt32("PROBE_SESSION_MAX_PER_USER", cfg.SessionMaxPerUser); err != nil {
		return Config{}, err
	}
	if cfg.SessionRetention, err = envDuration("PROBE_SESSION_REVOKED_RETENTION", cfg.SessionRetention); err != nil {
		return Config{}, err
	}
	if cfg.LoginIPLimit, err = envInt32("PROBE_LOGIN_IP_LIMIT", cfg.LoginIPLimit); err != nil {
		return Config{}, err
	}
	if cfg.LoginIPWindow, err = envDuration("PROBE_LOGIN_IP_WINDOW", cfg.LoginIPWindow); err != nil {
		return Config{}, err
	}
	if cfg.LoginUsernameLimit, err = envInt32("PROBE_LOGIN_USERNAME_LIMIT", cfg.LoginUsernameLimit); err != nil {
		return Config{}, err
	}
	if cfg.LoginUsernameWindow, err = envDuration("PROBE_LOGIN_USERNAME_WINDOW", cfg.LoginUsernameWindow); err != nil {
		return Config{}, err
	}
	if cfg.AgentEnrollIPLimit, err = envInt32("PROBE_AGENT_ENROLL_IP_LIMIT", cfg.AgentEnrollIPLimit); err != nil {
		return Config{}, err
	}
	if cfg.AgentRuntimeIPLimit, err = envInt32("PROBE_AGENT_RUNTIME_IP_LIMIT", cfg.AgentRuntimeIPLimit); err != nil {
		return Config{}, err
	}
	if cfg.AgentNodeLimit, err = envInt32("PROBE_AGENT_NODE_LIMIT", cfg.AgentNodeLimit); err != nil {
		return Config{}, err
	}
	if cfg.AgentRateWindow, err = envDuration("PROBE_AGENT_RATE_WINDOW", cfg.AgentRateWindow); err != nil {
		return Config{}, err
	}
	if cfg.RateLimitMaxKeysPerBucket, err = envInt32("PROBE_RATE_LIMIT_MAX_KEYS_PER_BUCKET", cfg.RateLimitMaxKeysPerBucket); err != nil {
		return Config{}, err
	}
	if cfg.NodeOfflineAfter, err = envDuration("PROBE_NODE_OFFLINE_AFTER", cfg.NodeOfflineAfter); err != nil {
		return Config{}, err
	}

	if err := validateListenAddress(cfg.ListenAddress); err != nil {
		return Config{}, err
	}
	if cfg.DatabaseMaxConns < 1 {
		return Config{}, errors.New("PROBE_DATABASE_MAX_CONNS must be at least 1")
	}
	if cfg.DatabaseMinConns < 0 || cfg.DatabaseMinConns > cfg.DatabaseMaxConns {
		return Config{}, errors.New("PROBE_DATABASE_MIN_CONNS must be between 0 and PROBE_DATABASE_MAX_CONNS")
	}
	if err := validateAdminOrigin(cfg.AdminOrigin); err != nil {
		return Config{}, err
	}
	if err := validateHTTPSOrigin("PROBE_AGENT_PUBLIC_URL", cfg.AgentPublicURL); err != nil {
		return Config{}, err
	}
	if err := validateAgentInstallerURL(cfg.AgentInstallerURL); err != nil {
		return Config{}, err
	}
	if cfg.AgentInstallCAFile != "" {
		cfg.AgentInstallCAPEM, err = loadPublicCABundle(cfg.AgentInstallCAFile)
		if err != nil {
			return Config{}, fmt.Errorf("PROBE_AGENT_INSTALL_CA_FILE: %w", err)
		}
	}
	if cfg.SessionTTL < 5*time.Minute || cfg.SessionTTL > 7*24*time.Hour {
		return Config{}, errors.New("PROBE_SESSION_TTL must be between 5m and 168h")
	}
	if cfg.SessionMaxPerUser < 1 || cfg.SessionMaxPerUser > 20 {
		return Config{}, errors.New("PROBE_SESSION_MAX_PER_USER must be between 1 and 20")
	}
	if cfg.SessionRetention < time.Hour || cfg.SessionRetention > 30*24*time.Hour {
		return Config{}, errors.New("PROBE_SESSION_REVOKED_RETENTION must be between 1h and 720h")
	}
	if cfg.LoginIPLimit < 1 || cfg.LoginIPLimit > 10000 {
		return Config{}, errors.New("PROBE_LOGIN_IP_LIMIT must be between 1 and 10000")
	}
	if cfg.LoginUsernameLimit < 1 || cfg.LoginUsernameLimit > 10000 {
		return Config{}, errors.New("PROBE_LOGIN_USERNAME_LIMIT must be between 1 and 10000")
	}
	if cfg.LoginIPWindow < time.Second || cfg.LoginIPWindow > 24*time.Hour ||
		cfg.LoginUsernameWindow < time.Second || cfg.LoginUsernameWindow > 24*time.Hour {
		return Config{}, errors.New("login rate-limit windows must be between 1s and 24h")
	}
	if cfg.AgentEnrollIPLimit < 1 || cfg.AgentRuntimeIPLimit < 1 || cfg.AgentNodeLimit < 1 ||
		cfg.AgentEnrollIPLimit > 100000 || cfg.AgentRuntimeIPLimit > 100000 || cfg.AgentNodeLimit > 100000 {
		return Config{}, errors.New("Agent rate limits must be between 1 and 100000")
	}
	if cfg.AgentRateWindow < time.Second || cfg.AgentRateWindow > time.Hour {
		return Config{}, errors.New("PROBE_AGENT_RATE_WINDOW must be between 1s and 1h")
	}
	if cfg.RateLimitMaxKeysPerBucket < 100 || cfg.RateLimitMaxKeysPerBucket > 1000000 {
		return Config{}, errors.New("PROBE_RATE_LIMIT_MAX_KEYS_PER_BUCKET must be between 100 and 1000000")
	}
	if cfg.NodeOfflineAfter < 10*time.Second || cfg.NodeOfflineAfter > 24*time.Hour {
		return Config{}, errors.New("PROBE_NODE_OFFLINE_AFTER must be between 10s and 24h")
	}
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("unsupported PROBE_LOG_LEVEL %q", cfg.LogLevel)
	}

	return cfg, nil
}

func validateListenAddress(value string) error {
	host, portValue, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil || host == "" {
		return errors.New("PROBE_API_LISTEN_ADDR must be an explicit loopback IP and port")
	}
	address, err := netip.ParseAddr(host)
	if err != nil || address.Zone() != "" || !address.Unmap().IsLoopback() {
		return errors.New("PROBE_API_LISTEN_ADDR must use a loopback IP address")
	}
	port, err := strconv.ParseUint(portValue, 10, 16)
	if err != nil || port == 0 {
		return errors.New("PROBE_API_LISTEN_ADDR port must be between 1 and 65535")
	}
	return nil
}

func validateAdminOrigin(value string) error {
	return validateHTTPSOrigin("PROBE_ADMIN_ORIGIN", value)
}

func validateHTTPSOrigin(name, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute https origin", name)
	}
	if parsed.User != nil || parsed.Opaque != "" || parsed.Path != "" || parsed.RawPath != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.String() != value {
		return fmt.Errorf("%s must contain only scheme and host", name)
	}
	return nil
}

func validateHTTPSResourceURL(name, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute https URL", name)
	}
	if parsed.User != nil || parsed.Opaque != "" || parsed.Path == "" || parsed.Path == "/" ||
		parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		strings.HasSuffix(parsed.Path, "/") || path.Clean(parsed.Path) != parsed.Path || parsed.String() != value {
		return fmt.Errorf("%s must be a canonical https file URL without credentials, query, or fragment", name)
	}
	return nil
}

func validateAgentInstallerURL(value string) error {
	const (
		name       = "PROBE_AGENT_INSTALLER_URL"
		pathPrefix = "/Kcmose/my-agent/"
		pathSuffix = "/deploy/install.sh"
	)
	if err := validateHTTPSResourceURL(name, value); err != nil {
		return err
	}
	parsed, _ := url.Parse(value)
	if parsed.Host != "raw.githubusercontent.com" || !strings.HasPrefix(parsed.Path, pathPrefix) ||
		!strings.HasSuffix(parsed.Path, pathSuffix) {
		return errors.New("PROBE_AGENT_INSTALLER_URL must use the Kcmose/my-agent GitHub Raw install script at an immutable revision")
	}
	revision := strings.TrimSuffix(strings.TrimPrefix(parsed.Path, pathPrefix), pathSuffix)
	isFullCommit := len(revision) == 40 && strings.Trim(revision, "0123456789abcdef") == ""
	version := strings.TrimPrefix(revision, "refs/tags/")
	isReleaseTag := version != revision && agentInstallerVersionPattern.MatchString(version)
	if !isFullCommit && !isReleaseTag {
		return errors.New("PROBE_AGENT_INSTALLER_URL must pin a full lowercase Git commit or refs/tags/vMAJOR.MINOR.PATCH release")
	}
	return nil
}

func loadPublicCABundle(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("must be an absolute path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("read metadata: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("must name a regular file, not a symlink")
	}
	if info.Size() < 1 || info.Size() > 64*1024 {
		return nil, errors.New("must contain between 1 byte and 64 KiB")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	rest := contents
	certificates := 0
	for len(strings.TrimSpace(string(rest))) > 0 {
		block, remaining := pem.Decode(rest)
		if block == nil {
			return nil, errors.New("must contain only PEM CERTIFICATE blocks")
		}
		if block.Type != "CERTIFICATE" {
			return nil, errors.New("must contain only public CERTIFICATE blocks")
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return nil, fmt.Errorf("parse certificate: %w", err)
		}
		certificates++
		rest = remaining
	}
	if certificates == 0 {
		return nil, errors.New("must contain at least one certificate")
	}
	return contents, nil
}

func (c Config) ValidateDatabase() error {
	if c.DatabaseURL == "" {
		return errors.New("PROBE_DATABASE_URL is required")
	}
	return nil
}

func (c Config) ValidateServe() error {
	if c.AgentPublicURL == "" || c.AgentPublicURL == "https://api.example.com" {
		return errors.New("PROBE_AGENT_PUBLIC_URL must be set to the deployed Agent HTTPS origin before serving")
	}
	if c.AgentInstallerURL == "" {
		return errors.New("PROBE_AGENT_INSTALLER_URL must name the published Agent installer before serving")
	}
	return nil
}

func envString(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return parsed, nil
}

func envInt32(name string, fallback int32) (int32, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%s must be a 32-bit integer", name)
	}
	return int32(parsed), nil
}
