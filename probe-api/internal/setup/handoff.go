package setup

import (
	"bytes"
	"errors"
	"net/netip"
	"net/url"
	"strconv"
)

const DefaultAPIEnvironmentPath = "/srv/probe/config/probe-api.env"

type installedAccessMetadata struct {
	Mode     IngressMode
	AdminURL string
}

type installedAccessLoader func(string) (installedAccessMetadata, error)

func loadInstalledAccess(path string) (installedAccessMetadata, error) {
	contents, err := readStableRegularFile(path, 128*1024)
	if err != nil {
		return installedAccessMetadata{}, err
	}
	defer clear(contents)
	wanted := map[string]string{}
	for _, rawLine := range bytes.Split(contents, []byte{'\n'}) {
		line := bytes.TrimSuffix(rawLine, []byte{'\r'})
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		keyBytes, value, ok := bytes.Cut(line, []byte{'='})
		if !ok {
			return installedAccessMetadata{}, errors.New("installed API environment is malformed")
		}
		key := string(keyBytes)
		switch key {
		case "PROBE_INSTALLATION_PROFILE", "PROBE_INGRESS_MODE", "PROBE_ADMIN_ORIGIN", "PROBE_AGENT_PUBLIC_URL", "PROBE_AGENT_INSTALLER_URL", "PROBE_AGENT_INSTALL_CA_FILE":
			if _, exists := wanted[key]; exists {
				return installedAccessMetadata{}, errors.New("installed API environment contains a duplicate handoff key")
			}
			wanted[key] = string(value)
		}
	}
	for _, key := range []string{"PROBE_INGRESS_MODE", "PROBE_ADMIN_ORIGIN"} {
		if _, exists := wanted[key]; !exists {
			return installedAccessMetadata{}, errors.New("installed API environment is missing handoff metadata")
		}
	}
	profile := InstallationProfileFull
	if value, exists := wanted["PROBE_INSTALLATION_PROFILE"]; exists {
		profile, err = ParseInstallationProfile(value)
		if err != nil {
			return installedAccessMetadata{}, errors.New("installed setup profile is invalid")
		}
	}
	if profile == InstallationProfileFull {
		for _, key := range []string{"PROBE_AGENT_PUBLIC_URL", "PROBE_AGENT_INSTALL_CA_FILE"} {
			if _, exists := wanted[key]; !exists {
				return installedAccessMetadata{}, errors.New("installed API environment is missing handoff metadata")
			}
		}
	}

	mode := IngressMode(wanted["PROBE_INGRESS_MODE"])
	switch mode {
	case IngressModeIP:
		if profile == InstallationProfileManagement {
			_, adminOrigin, err := validateInstalledIPOrigin(wanted["PROBE_ADMIN_ORIGIN"], AdminHTTPSPort)
			if err != nil {
				return installedAccessMetadata{}, err
			}
			return installedAccessMetadata{Mode: mode, AdminURL: adminOrigin + "/login"}, nil
		}
		if wanted["PROBE_AGENT_INSTALL_CA_FILE"] != DefaultPrivateCACertificatePath {
			return installedAccessMetadata{}, errors.New("installed private CA path is invalid")
		}
		adminAddress, adminOrigin, err := validateInstalledIPOrigin(wanted["PROBE_ADMIN_ORIGIN"], AdminHTTPSPort)
		if err != nil {
			return installedAccessMetadata{}, err
		}
		agentAddress, _, err := validateInstalledIPOrigin(wanted["PROBE_AGENT_PUBLIC_URL"], AgentHTTPSPort)
		if err != nil || agentAddress != adminAddress {
			return installedAccessMetadata{}, errors.New("installed IP origins are inconsistent")
		}
		return installedAccessMetadata{Mode: mode, AdminURL: adminOrigin + "/login"}, nil
	case IngressModeDomain:
		if profile == InstallationProfileManagement {
			adminOrigin, err := validateInstalledDomainOrigin(wanted["PROBE_ADMIN_ORIGIN"])
			if err != nil {
				return installedAccessMetadata{}, err
			}
			return installedAccessMetadata{Mode: mode, AdminURL: adminOrigin + "/login"}, nil
		}
		if wanted["PROBE_AGENT_INSTALL_CA_FILE"] != "" {
			return installedAccessMetadata{}, errors.New("installed domain mode unexpectedly configures a private CA")
		}
		adminOrigin, err := validateInstalledDomainOrigin(wanted["PROBE_ADMIN_ORIGIN"])
		if err != nil {
			return installedAccessMetadata{}, err
		}
		if _, err := validateInstalledDomainOrigin(wanted["PROBE_AGENT_PUBLIC_URL"]); err != nil {
			return installedAccessMetadata{}, err
		}
		return installedAccessMetadata{Mode: mode, AdminURL: adminOrigin + "/login"}, nil
	default:
		return installedAccessMetadata{}, errors.New("installed ingress mode is invalid")
	}
}

func validateInstalledIPOrigin(value string, port int) (netip.Addr, string, error) {
	parsed, err := parseInstalledHTTPSOrigin(value)
	if err != nil || parsed.Port() != strconv.Itoa(port) {
		return netip.Addr{}, "", errors.New("installed IP origin is invalid")
	}
	address, err := validateNetworkAddress(parsed.Hostname())
	if err != nil {
		return netip.Addr{}, "", errors.New("installed IP origin is invalid")
	}
	origin := ipOrigin(address, port)
	if value != origin {
		return netip.Addr{}, "", errors.New("installed IP origin is not canonical")
	}
	return address, origin, nil
}

func validateInstalledDomainOrigin(value string) (string, error) {
	parsed, err := parseInstalledHTTPSOrigin(value)
	if err != nil || parsed.Port() != "" {
		return "", errors.New("installed domain origin is invalid")
	}
	host, err := validateDomain(parsed.Hostname(), "installed origin")
	if err != nil || value != "https://"+host {
		return "", errors.New("installed domain origin is invalid")
	}
	return value, nil
}

func parseInstalledHTTPSOrigin(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, errors.New("installed HTTPS origin is invalid")
	}
	return parsed, nil
}
