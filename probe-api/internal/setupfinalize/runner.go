package setupfinalize

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type Runner interface {
	Run(context.Context, string, ...string) error
	RunQuiet(context.Context, string, ...string) error
	RunSensitive(context.Context, []byte, string, ...string) error
	Output(context.Context, string, ...string) ([]byte, error)
}

type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("command %s failed", filepathBase(name))
	}
	return nil
}

func (OSRunner) RunQuiet(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return fmt.Errorf("command %s failed", filepathBase(name))
	}
	return nil
}

func (OSRunner) RunSensitive(ctx context.Context, stdin []byte, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = bytes.NewReader(stdin)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return fmt.Errorf("sensitive command %s failed without output", filepathBase(name))
	}
	return nil
}

func (OSRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	// Output commands are limited to listener inspection, local PostgreSQL
	// catalog checks, and closed systemd rollback properties; none receives a
	// password. Preserve stderr in the root-only systemd journal so an
	// installation failure is diagnosable without reflecting any submitted
	// secret through the setup HTTP protocol.
	command.Stderr = os.Stderr
	output, err := command.Output()
	if err != nil {
		clear(output)
		return nil, fmt.Errorf("command %s failed", filepathBase(name))
	}
	return output, nil
}

func filepathBase(value string) string {
	if index := strings.LastIndexByte(value, '/'); index >= 0 {
		return value[index+1:]
	}
	return value
}

func wildcardListener(output []byte, port string) bool {
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		address := fields[3]
		if listenerPort(address) != port {
			continue
		}
		if strings.HasPrefix(address, "0.0.0.0:") || strings.HasPrefix(address, "[::]:") || strings.HasPrefix(address, "*:") {
			return true
		}
	}
	return false
}

func tcpListenerPorts(output []byte) (map[string]struct{}, error) {
	ports := make(map[string]struct{})
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "LISTEN" {
			return nil, fmt.Errorf("unexpected ss listener record")
		}
		_, _, port, splitErr := splitSSListenerAddress(fields[3])
		if splitErr != nil {
			return nil, fmt.Errorf("unexpected ss listener address")
		}
		parsed, err := strconv.ParseUint(port, 10, 16)
		if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != port {
			return nil, fmt.Errorf("unexpected ss listener address")
		}
		ports[strconv.FormatUint(parsed, 10)] = struct{}{}
	}
	return ports, nil
}

func splitSSListenerAddress(value string) (string, string, string, error) {
	hostWithZone := ""
	outsideZone := ""
	outsideZoneSpecified := false
	port := ""
	bracketed := strings.HasPrefix(value, "[")
	if bracketed {
		closingBracket := strings.IndexByte(value, ']')
		if closingBracket <= 1 {
			return "", "", "", fmt.Errorf("invalid bracketed listener address")
		}
		hostWithZone = value[1:closingBracket]
		suffix := value[closingBracket+1:]
		switch {
		case strings.HasPrefix(suffix, ":"):
			port = suffix[1:]
		case strings.HasPrefix(suffix, "%"):
			portSeparator := strings.LastIndexByte(suffix, ':')
			if portSeparator <= 1 {
				return "", "", "", fmt.Errorf("invalid scoped listener address")
			}
			outsideZone = suffix[1:portSeparator]
			outsideZoneSpecified = true
			port = suffix[portSeparator+1:]
		default:
			return "", "", "", fmt.Errorf("invalid bracketed listener suffix")
		}
	} else {
		portSeparator := strings.LastIndexByte(value, ':')
		if portSeparator <= 0 {
			return "", "", "", fmt.Errorf("invalid listener address")
		}
		hostWithZone = value[:portSeparator]
		port = value[portSeparator+1:]
	}

	host := hostWithZone
	zone := ""
	insideZoneSpecified := false
	if zoneSeparator := strings.IndexByte(hostWithZone, '%'); zoneSeparator >= 0 {
		host = hostWithZone[:zoneSeparator]
		zone = hostWithZone[zoneSeparator+1:]
		insideZoneSpecified = true
	}
	if outsideZoneSpecified {
		if insideZoneSpecified {
			return "", "", "", fmt.Errorf("ambiguous listener IP zone")
		}
		zone = outsideZone
	}
	zoneSpecified := insideZoneSpecified || outsideZoneSpecified
	if zoneSpecified && !validListenerZone(zone) {
		return "", "", "", fmt.Errorf("invalid listener IP zone")
	}
	if host == "*" {
		if bracketed {
			return "", "", "", fmt.Errorf("invalid wildcard listener address")
		}
		return host, zone, port, nil
	}
	address, err := netip.ParseAddr(host)
	if err != nil || !address.IsValid() || address.Is4In6() || (bracketed && address.Is4()) {
		return "", "", "", fmt.Errorf("invalid listener IP address")
	}
	return host, zone, port, nil
}

func validListenerZone(zone string) bool {
	// Linux interface names are limited to IFNAMSIZ-1 bytes. Match the relevant
	// dev_valid_name boundary while rejecting NUL and ASCII whitespace
	// delimiters so malformed ss(8) output still fails closed.
	if len(zone) == 0 || len(zone) > 15 || zone == "." || zone == ".." {
		return false
	}
	for index := 0; index < len(zone); index++ {
		character := zone[index]
		if character == 0 || character == '/' || character == ':' ||
			character == ' ' || character == '\t' || character == '\n' ||
			character == '\r' || character == '\v' || character == '\f' {
			return false
		}
	}
	return true
}

// tcpPortRestrictedToLoopback accepts a target port only when at least one
// listener exists and every listener for that port is exactly IPv4 localhost
// or IPv6 localhost. The preliminary parser validates every non-empty ss row,
// including unrelated ports, so malformed inspection output fails closed.
func tcpPortRestrictedToLoopback(output []byte, targetPort string) (bool, error) {
	if targetPort == "" {
		return false, fmt.Errorf("target listener port is required")
	}
	if _, err := tcpListenerPorts(output); err != nil {
		return false, err
	}
	ipv4Loopback := netip.AddrFrom4([4]byte{127, 0, 0, 1})
	ipv6Loopback := netip.IPv6Loopback()
	found := false
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		host, zone, port, err := splitSSListenerAddress(fields[3])
		if err != nil {
			return false, fmt.Errorf("unexpected ss listener address")
		}
		if port != targetPort {
			continue
		}
		if host == "*" || zone != "" {
			return false, nil
		}
		address, err := netip.ParseAddr(host)
		if err != nil {
			return false, fmt.Errorf("unexpected ss listener address")
		}
		if address != ipv4Loopback && address != ipv6Loopback {
			return false, nil
		}
		found = true
	}
	return found, nil
}

func listenerPort(address string) string {
	index := strings.LastIndexByte(address, ':')
	if index < 0 {
		return ""
	}
	return strings.TrimSuffix(address[index+1:], "]")
}
