// Package ingresstls validates the immutable production ingress certificate
// contract without changing certificate, Nginx, or systemd state.
package ingresstls

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strings"
	"time"
)

const maxPEMFileSize = 1024 * 1024

type CertificatePair struct {
	Certificate string
	PrivateKey  string
}

type Paths struct {
	Panel                       CertificatePair
	Admin                       CertificatePair
	Agent                       CertificatePair
	Private                     CertificatePair
	PrivateCertificateAuthority string
}

type DomainConfig struct {
	Paths       Paths
	PanelHost   string
	AdminHost   string
	AgentHost   string
	Roots       *x509.CertPool
	CurrentTime time.Time
}

type IPConfig struct {
	Paths       Paths
	Address     string
	CurrentTime time.Time
}

func ProductionPaths() Paths {
	return Paths{
		Panel: CertificatePair{
			Certificate: "/etc/probe-panel/tls/panel/fullchain.pem",
			PrivateKey:  "/etc/probe-panel/tls/panel/privkey.pem",
		},
		Admin: CertificatePair{
			Certificate: "/etc/probe-panel/tls/admin/fullchain.pem",
			PrivateKey:  "/etc/probe-panel/tls/admin/privkey.pem",
		},
		Agent: CertificatePair{
			Certificate: "/etc/probe-panel/tls/api/fullchain.pem",
			PrivateKey:  "/etc/probe-panel/tls/api/privkey.pem",
		},
		Private: CertificatePair{
			Certificate: "/etc/probe-panel/tls/private-ca/fullchain.pem",
			PrivateKey:  "/etc/probe-panel/tls/private-ca/privkey.pem",
		},
		PrivateCertificateAuthority: "/etc/probe-panel/tls/private-ca/ca.pem",
	}
}

func ValidateDomain(config DomainConfig) error {
	now := currentTime(config.CurrentTime)
	roots := config.Roots
	if roots == nil {
		var err error
		roots, err = x509.SystemCertPool()
		if err != nil || roots == nil {
			return errors.New("load system certificate roots")
		}
	}

	entries := []struct {
		name string
		host string
		pair CertificatePair
	}{
		{name: "panel", host: config.PanelHost, pair: config.Paths.Panel},
		{name: "admin", host: config.AdminHost, pair: config.Paths.Admin},
		{name: "agent", host: config.AgentHost, pair: config.Paths.Agent},
	}
	for _, entry := range entries {
		if err := validateCanonicalHostname(entry.host); err != nil {
			return fmt.Errorf("%s ingress hostname: %w", entry.name, err)
		}
		certificates, err := loadKeyPair(entry.pair)
		if err != nil {
			return fmt.Errorf("%s ingress TLS: %w", entry.name, err)
		}
		leaf := certificates[0]
		if err := validateServerLeaf(leaf, now); err != nil {
			return fmt.Errorf("%s ingress TLS: %w", entry.name, err)
		}
		if err := leaf.VerifyHostname(entry.host); err != nil {
			return fmt.Errorf("%s ingress TLS certificate does not contain the expected DNS SAN", entry.name)
		}
		intermediates := x509.NewCertPool()
		for _, certificate := range certificates[1:] {
			intermediates.AddCert(certificate)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{
			DNSName:       entry.host,
			Roots:         roots,
			Intermediates: intermediates,
			CurrentTime:   now,
			KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}); err != nil {
			return fmt.Errorf("%s ingress TLS public chain verification failed: %w", entry.name, err)
		}
	}
	return nil
}

func ValidateIP(config IPConfig) error {
	address, err := canonicalAddress(config.Address)
	if err != nil {
		return err
	}
	now := currentTime(config.CurrentTime)
	certificates, err := loadKeyPair(config.Paths.Private)
	if err != nil {
		return fmt.Errorf("private ingress TLS: %w", err)
	}
	leaf := certificates[0]
	if err := validateServerLeaf(leaf, now); err != nil {
		return fmt.Errorf("private ingress TLS: %w", err)
	}
	if err := validateExactIPSAN(leaf, address); err != nil {
		return err
	}
	if err := leaf.VerifyHostname(address.String()); err != nil {
		return errors.New("private ingress TLS leaf does not cover the configured address")
	}

	caPEM, err := readSmallFile(config.Paths.PrivateCertificateAuthority)
	if err != nil {
		return fmt.Errorf("private ingress CA: %w", err)
	}
	ca, err := parseSingleCertificate(caPEM)
	if err != nil {
		return fmt.Errorf("private ingress CA: %w", err)
	}
	if !ca.BasicConstraintsValid || !ca.IsCA || ca.KeyUsage&x509.KeyUsageCertSign == 0 {
		return errors.New("private ingress CA must be a valid certificate-signing CA")
	}
	if err := validAt(ca, now); err != nil {
		return fmt.Errorf("private ingress CA: %w", err)
	}
	if err := ca.CheckSignatureFrom(ca); err != nil {
		return errors.New("private ingress CA must be self-signed")
	}
	if len(certificates) != 2 || !certificates[1].Equal(ca) {
		return errors.New("private ingress fullchain must contain exactly the leaf and fixed CA")
	}
	if err := leaf.CheckSignatureFrom(ca); err != nil {
		return errors.New("private ingress TLS leaf is not directly signed by the fixed CA")
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{
		DNSName:     address.String(),
		Roots:       roots,
		CurrentTime: now,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return fmt.Errorf("private ingress TLS chain verification failed: %w", err)
	}
	return nil
}

func loadKeyPair(paths CertificatePair) ([]*x509.Certificate, error) {
	if paths.Certificate == "" || paths.PrivateKey == "" {
		return nil, errors.New("certificate paths are incomplete")
	}
	certificatePEM, err := readSmallFile(paths.Certificate)
	if err != nil {
		return nil, fmt.Errorf("read certificate: %w", err)
	}
	privateKeyPEM, err := readSmallFile(paths.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	pair, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil || len(pair.Certificate) == 0 {
		return nil, errors.New("certificate and private key are invalid or mismatched")
	}
	certificates := make([]*x509.Certificate, 0, len(pair.Certificate))
	for _, encoded := range pair.Certificate {
		certificate, parseErr := x509.ParseCertificate(encoded)
		if parseErr != nil {
			return nil, errors.New("certificate chain cannot be parsed")
		}
		certificates = append(certificates, certificate)
	}
	return certificates, nil
}

func validateServerLeaf(certificate *x509.Certificate, now time.Time) error {
	if certificate.IsCA {
		return errors.New("leaf certificate cannot be a CA")
	}
	if err := validAt(certificate, now); err != nil {
		return err
	}
	for _, usage := range certificate.ExtKeyUsage {
		if usage == x509.ExtKeyUsageServerAuth {
			return nil
		}
	}
	return errors.New("leaf certificate must explicitly allow TLS server authentication")
}

func validateExactIPSAN(certificate *x509.Certificate, address netip.Addr) error {
	if len(certificate.DNSNames) != 0 || len(certificate.EmailAddresses) != 0 || len(certificate.URIs) != 0 || len(certificate.IPAddresses) != 1 {
		return errors.New("private ingress TLS leaf must contain only one IP SAN")
	}
	sanAddress, ok := netip.AddrFromSlice(certificate.IPAddresses[0])
	if !ok || sanAddress.Unmap() != address {
		return errors.New("private ingress TLS leaf IP SAN does not match the configured address")
	}

	var sanExtension []byte
	for _, extension := range certificate.Extensions {
		if extension.Id.Equal(asn1.ObjectIdentifier{2, 5, 29, 17}) {
			if sanExtension != nil {
				return errors.New("private ingress TLS leaf contains duplicate SAN extensions")
			}
			sanExtension = extension.Value
		}
	}
	if sanExtension == nil {
		return errors.New("private ingress TLS leaf is missing its IP SAN extension")
	}
	var generalNames []asn1.RawValue
	rest, err := asn1.Unmarshal(sanExtension, &generalNames)
	if err != nil || len(rest) != 0 || len(generalNames) != 1 {
		return errors.New("private ingress TLS leaf must contain exactly one SAN entry")
	}
	entry := generalNames[0]
	if entry.Class != asn1.ClassContextSpecific || entry.Tag != 7 || entry.IsCompound || !bytes.Equal(entry.Bytes, address.AsSlice()) {
		return errors.New("private ingress TLS leaf SAN must be exactly the configured IP address")
	}
	return nil
}

func validAt(certificate *x509.Certificate, now time.Time) error {
	if now.Before(certificate.NotBefore) {
		return errors.New("certificate is not valid yet")
	}
	if !now.Before(certificate.NotAfter) {
		return errors.New("certificate is expired")
	}
	return nil
}

func canonicalAddress(value string) (netip.Addr, error) {
	address, err := netip.ParseAddr(value)
	if err != nil || value == "" || address.String() != value || address.Zone() != "" || address.Is4In6() || !address.IsGlobalUnicast() {
		return netip.Addr{}, errors.New("ingress address must be a canonical routable IPv4 or IPv6 address")
	}
	return address, nil
}

func validateCanonicalHostname(hostname string) error {
	if hostname == "" || hostname != strings.ToLower(hostname) || len(hostname) > 253 || strings.HasSuffix(hostname, ".") {
		return errors.New("must be a canonical lowercase DNS hostname")
	}
	if _, err := netip.ParseAddr(hostname); err == nil {
		return errors.New("must be a canonical lowercase DNS hostname, not an IP address")
	}
	if hostname == "example.com" || strings.HasSuffix(hostname, ".example.com") {
		return errors.New("must not use the reserved example.com domain")
	}
	labels := strings.Split(hostname, ".")
	if len(labels) < 2 {
		return errors.New("must be a canonical lowercase DNS hostname")
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("must be a canonical lowercase DNS hostname")
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return errors.New("must be a canonical lowercase DNS hostname")
			}
		}
	}
	return nil
}

func parseSingleCertificate(contents []byte) (*x509.Certificate, error) {
	rest := bytes.TrimSpace(contents)
	block, trailing := pem.Decode(rest)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(trailing)) != 0 {
		return nil, errors.New("CA file must contain exactly one PEM certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.New("CA certificate cannot be parsed")
	}
	return certificate, nil
}

func readSmallFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxPEMFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(contents) == 0 || len(contents) > maxPEMFileSize {
		return nil, errors.New("PEM file is empty or too large")
	}
	return contents, nil
}

func currentTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}
