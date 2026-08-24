package access

import (
	"bufio"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"
)

const maxAllowlistEntries = 4096

// CIDRSet is an immutable, normalized collection of IP prefixes.
// Its zero value is a valid empty set and therefore matches no address.
type CIDRSet struct {
	prefixes []netip.Prefix
}

func (set CIDRSet) Contains(address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	for _, prefix := range set.prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func (set CIDRSet) Empty() bool {
	return len(set.prefixes) == 0
}

func (set CIDRSet) Len() int {
	return len(set.prefixes)
}

// ParseCIDRList parses a comma-separated list used for trusted proxy
// configuration. Individual IP addresses are accepted and normalized to
// /32 or /128. Default routes are always rejected.
func ParseCIDRList(value string) (CIDRSet, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return CIDRSet{}, nil
	}
	parts := strings.Split(value, ",")
	if len(parts) > maxAllowlistEntries {
		return CIDRSet{}, fmt.Errorf("CIDR list contains more than %d entries", maxAllowlistEntries)
	}
	prefixes := make([]netip.Prefix, 0, len(parts))
	seen := make(map[netip.Prefix]struct{}, len(parts))
	for index, part := range parts {
		prefix, err := parsePrefix(strings.TrimSpace(part), true)
		if err != nil {
			return CIDRSet{}, fmt.Errorf("CIDR entry %d: %w", index+1, err)
		}
		if _, exists := seen[prefix]; exists {
			return CIDRSet{}, fmt.Errorf("CIDR entry %d duplicates %s", index+1, prefix)
		}
		seen[prefix] = struct{}{}
		prefixes = append(prefixes, prefix)
	}
	return CIDRSet{prefixes: prefixes}, nil
}

// LoadNginxGeoAllowlist parses the exact include file consumed by Nginx's
// geo directive. Only explicit "IP-or-CIDR 1;" records and comments are
// accepted, so an invalid or overly broad rule cannot reach nginx -s reload.
func LoadNginxGeoAllowlist(path string) (CIDRSet, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return CIDRSet{}, errors.New("allowlist path cannot be empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return CIDRSet{}, fmt.Errorf("open allowlist: %w", err)
	}
	defer file.Close()

	prefixes := make([]netip.Prefix, 0)
	seen := make(map[netip.Prefix]struct{})
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if comment := strings.IndexByte(line, '#'); comment >= 0 {
			line = line[:comment]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != "1;" {
			return CIDRSet{}, fmt.Errorf("allowlist line %d must be 'IP-or-CIDR 1;'", lineNumber)
		}
		prefix, err := parsePrefix(fields[0], false)
		if err != nil {
			return CIDRSet{}, fmt.Errorf("allowlist line %d: %w", lineNumber, err)
		}
		if _, exists := seen[prefix]; exists {
			return CIDRSet{}, fmt.Errorf("allowlist line %d duplicates %s", lineNumber, prefix)
		}
		if len(prefixes) >= maxAllowlistEntries {
			return CIDRSet{}, fmt.Errorf("allowlist contains more than %d entries", maxAllowlistEntries)
		}
		seen[prefix] = struct{}{}
		prefixes = append(prefixes, prefix)
	}
	if err := scanner.Err(); err != nil {
		return CIDRSet{}, fmt.Errorf("read allowlist: %w", err)
	}
	return CIDRSet{prefixes: prefixes}, nil
}

func parsePrefix(value string, allowMapped bool) (netip.Prefix, error) {
	if value == "" || strings.Contains(value, "%") {
		return netip.Prefix{}, errors.New("IP address or CIDR is invalid")
	}
	if address, err := netip.ParseAddr(value); err == nil {
		if address.Is4In6() && !allowMapped {
			return netip.Prefix{}, errors.New("IPv4-mapped IPv6 is not allowed in the shared Nginx file")
		}
		address = address.Unmap()
		return netip.PrefixFrom(address, address.BitLen()), nil
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil || !prefix.IsValid() || prefix.Addr().Zone() != "" {
		return netip.Prefix{}, errors.New("IP address or CIDR is invalid")
	}
	masked := prefix.Masked()
	if prefix != masked {
		return netip.Prefix{}, errors.New("CIDR must use its canonical network address")
	}
	prefix = masked
	if prefix.Addr().Is4In6() {
		if !allowMapped {
			return netip.Prefix{}, errors.New("IPv4-mapped IPv6 is not allowed in the shared Nginx file")
		}
		if prefix.Bits() < 96 {
			return netip.Prefix{}, errors.New("IPv4-mapped CIDR is invalid")
		}
		prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96).Masked()
	}
	if prefix.Bits() == 0 {
		return netip.Prefix{}, errors.New("default routes are forbidden")
	}
	return prefix, nil
}
