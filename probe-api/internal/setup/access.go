package setup

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
)

type IngressMode string

const (
	IngressModeDomain IngressMode = "domain"
	IngressModeIP     IngressMode = "ip"

	PanelHTTPSPort = 18453
	AgentHTTPSPort = 18454
	AdminHTTPSPort = 18455
)

// AccessConfiguration is the canonical, server-validated set of public HTTPS
// origins. Callers must not infer an origin independently from raw request
// fields because IPv6 literals require bracketed host:port formatting.
type AccessConfiguration struct {
	Mode        IngressMode
	Profile     InstallationProfile
	Address     netip.Addr
	PanelOrigin string
	AgentOrigin string
	AdminOrigin string
}

func (request CompleteRequest) AccessConfiguration() (AccessConfiguration, error) {
	profile, err := request.EffectiveProfile()
	if err != nil {
		return AccessConfiguration{}, err
	}
	if profile == InstallationProfileManagement {
		return request.managementAccessConfiguration()
	}

	domains := []string{request.Domains.Panel, request.Domains.Admin, request.Domains.Agent}
	nonEmpty := 0
	for _, domain := range domains {
		if domain != "" {
			nonEmpty++
		}
	}

	switch nonEmpty {
	case 0:
		address, err := validateNetworkAddress(request.Network.Address)
		if err != nil {
			return AccessConfiguration{}, err
		}
		if request.TLS.Mode != "private_ca" {
			return AccessConfiguration{}, errors.New("tls.mode must be private_ca when domains are empty")
		}
		if request.TLS.Email != "" {
			return AccessConfiguration{}, errors.New("tls.email must be empty in private_ca mode")
		}
		return AccessConfiguration{
			Mode:        IngressModeIP,
			Profile:     InstallationProfileFull,
			Address:     address,
			PanelOrigin: ipOrigin(address, PanelHTTPSPort),
			AgentOrigin: ipOrigin(address, AgentHTTPSPort),
			AdminOrigin: ipOrigin(address, AdminHTTPSPort),
		}, nil
	case 3:
		if request.Network.Address != "" {
			return AccessConfiguration{}, errors.New("network.address must be empty when domains are configured")
		}
		panelHost, err := validateDomain(request.Domains.Panel, "domains.panel")
		if err != nil {
			return AccessConfiguration{}, err
		}
		adminHost, err := validateDomain(request.Domains.Admin, "domains.admin")
		if err != nil {
			return AccessConfiguration{}, err
		}
		agentHost, err := validateDomain(request.Domains.Agent, "domains.agent")
		if err != nil {
			return AccessConfiguration{}, err
		}
		if domainsOverlap(panelHost, adminHost) || domainsOverlap(panelHost, agentHost) || domainsOverlap(adminHost, agentHost) {
			return AccessConfiguration{}, errors.New("panel, admin, and agent domains must be distinct and must not contain one another")
		}
		if request.TLS.Mode != "acme" {
			return AccessConfiguration{}, errors.New("tls.mode must be acme when all domains are configured")
		}
		if err := validateEmail(request.TLS.Email); err != nil {
			return AccessConfiguration{}, err
		}
		return AccessConfiguration{
			Mode:        IngressModeDomain,
			Profile:     InstallationProfileFull,
			PanelOrigin: "https://" + panelHost,
			AgentOrigin: "https://" + agentHost,
			AdminOrigin: "https://" + adminHost,
		}, nil
	default:
		return AccessConfiguration{}, errors.New("panel, admin, and agent domains must be either all empty or all configured")
	}
}

func (request CompleteRequest) managementAccessConfiguration() (AccessConfiguration, error) {
	if request.Domains.Panel != "" || request.Domains.Agent != "" {
		return AccessConfiguration{}, errors.New("management profile must not configure panel or agent domains")
	}
	if request.Domains.Admin == "" {
		address, err := validateNetworkAddress(request.Network.Address)
		if err != nil {
			return AccessConfiguration{}, err
		}
		if request.TLS.Mode != "private_ca" {
			return AccessConfiguration{}, errors.New("tls.mode must be private_ca when the management domain is empty")
		}
		if request.TLS.Email != "" {
			return AccessConfiguration{}, errors.New("tls.email must be empty in private_ca mode")
		}
		return AccessConfiguration{
			Mode:        IngressModeIP,
			Profile:     InstallationProfileManagement,
			Address:     address,
			AdminOrigin: ipOrigin(address, AdminHTTPSPort),
		}, nil
	}
	if request.Network.Address != "" {
		return AccessConfiguration{}, errors.New("network.address must be empty when the management domain is configured")
	}
	adminHost, err := validateDomain(request.Domains.Admin, "domains.admin")
	if err != nil {
		return AccessConfiguration{}, err
	}
	if request.TLS.Mode != "acme" {
		return AccessConfiguration{}, errors.New("tls.mode must be acme when the management domain is configured")
	}
	if err := validateEmail(request.TLS.Email); err != nil {
		return AccessConfiguration{}, err
	}
	return AccessConfiguration{
		Mode:        IngressModeDomain,
		Profile:     InstallationProfileManagement,
		AdminOrigin: "https://" + adminHost,
	}, nil
}

func validateNetworkAddress(value string) (netip.Addr, error) {
	address, err := netip.ParseAddr(value)
	if err != nil || value == "" || address.String() != value || address.Zone() != "" || address.Is4In6() || !address.IsGlobalUnicast() {
		return netip.Addr{}, errors.New("network.address must be a canonical routable IPv4 or IPv6 address")
	}
	return address, nil
}

func ipOrigin(address netip.Addr, port int) string {
	return "https://" + net.JoinHostPort(address.String(), strconv.Itoa(port))
}

func (configuration AccessConfiguration) HostPort(port int) (string, error) {
	if configuration.Mode != IngressModeIP || !configuration.Address.IsValid() {
		return "", fmt.Errorf("IP ingress is not configured")
	}
	return net.JoinHostPort(configuration.Address.String(), strconv.Itoa(port)), nil
}
