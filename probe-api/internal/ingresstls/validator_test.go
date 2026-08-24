package ingresstls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateDomainUsesExpectedSANServerAuthAndPublicChain(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	config := newDomainFixture(t, now, nil)
	if err := ValidateDomain(config); err != nil {
		t.Fatalf("ValidateDomain() error = %v", err)
	}

	for name, mutate := range map[string]func(*x509.Certificate){
		"wrong SAN": func(certificate *x509.Certificate) {
			certificate.DNSNames = []string{"other.example.net"}
		},
		"expired": func(certificate *x509.Certificate) {
			certificate.NotAfter = now.Add(-time.Minute)
		},
		"no ServerAuth": func(certificate *x509.Certificate) {
			certificate.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		},
	} {
		t.Run(name, func(t *testing.T) {
			invalid := newDomainFixture(t, now, mutate)
			if err := ValidateDomain(invalid); err == nil {
				t.Fatalf("ValidateDomain() accepted %s certificate", name)
			}
		})
	}

	t.Run("untrusted root", func(t *testing.T) {
		untrusted := newDomainFixture(t, now, nil)
		untrusted.Roots = x509.NewCertPool()
		if err := ValidateDomain(untrusted); err == nil || !strings.Contains(err.Error(), "public chain") {
			t.Fatalf("ValidateDomain() untrusted error = %v", err)
		}
	})

	t.Run("mismatched key", func(t *testing.T) {
		mismatched := newDomainFixture(t, now, nil)
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		writePrivateKey(t, mismatched.Paths.Panel.PrivateKey, key)
		if err := ValidateDomain(mismatched); err == nil || !strings.Contains(err.Error(), "mismatched") {
			t.Fatalf("ValidateDomain() mismatched-key error = %v", err)
		}
	})
}

func TestValidateIPRequiresExactSANAndFixedDirectCA(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	config := newIPFixture(t, now, nil, nil)
	if err := ValidateIP(config); err != nil {
		t.Fatalf("ValidateIP() error = %v", err)
	}
	ipv6 := newIPFixtureAddress(t, now, "2001:db8::1", nil, nil)
	if err := ValidateIP(ipv6); err != nil {
		t.Fatalf("ValidateIP(IPv6) error = %v", err)
	}

	for name, mutate := range map[string]func(*x509.Certificate){
		"DNS SAN": func(certificate *x509.Certificate) {
			certificate.DNSNames = []string{"panel.example.net"}
		},
		"extra IP SAN": func(certificate *x509.Certificate) {
			certificate.IPAddresses = append(certificate.IPAddresses, net.ParseIP("203.0.113.11"))
		},
		"wrong IP SAN": func(certificate *x509.Certificate) {
			certificate.IPAddresses = []net.IP{net.ParseIP("203.0.113.11")}
		},
		"no ServerAuth": func(certificate *x509.Certificate) {
			certificate.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		},
	} {
		t.Run(name, func(t *testing.T) {
			invalid := newIPFixture(t, now, mutate, nil)
			if err := ValidateIP(invalid); err == nil {
				t.Fatalf("ValidateIP() accepted %s certificate", name)
			}
		})
	}

	for name, mutate := range map[string]func(*x509.Certificate){
		"expired CA": func(certificate *x509.Certificate) {
			certificate.NotAfter = now.Add(-time.Minute)
		},
		"CA without cert-sign usage": func(certificate *x509.Certificate) {
			certificate.KeyUsage = x509.KeyUsageDigitalSignature
		},
		"non-CA": func(certificate *x509.Certificate) {
			certificate.IsCA = false
			certificate.KeyUsage = x509.KeyUsageDigitalSignature
			certificate.MaxPathLen = -1
			certificate.MaxPathLenZero = false
		},
	} {
		t.Run(name, func(t *testing.T) {
			invalid := newIPFixture(t, now, nil, mutate)
			if err := ValidateIP(invalid); err == nil {
				t.Fatalf("ValidateIP() accepted %s", name)
			}
		})
	}

	t.Run("non-canonical IPv6", func(t *testing.T) {
		invalid := newIPFixture(t, now, nil, nil)
		invalid.Address = "2001:0db8::1"
		if err := ValidateIP(invalid); err == nil || !strings.Contains(err.Error(), "canonical") {
			t.Fatalf("ValidateIP() non-canonical address error = %v", err)
		}
	})
}

func newDomainFixture(t *testing.T, now time.Time, mutatePanel func(*x509.Certificate)) DomainConfig {
	t.Helper()
	directory := t.TempDir()
	root, rootKey, _ := createAuthority(t, now, "public test root", nil, nil, func(template *x509.Certificate) {
		template.MaxPathLen = 1
	})
	intermediate, intermediateKey, intermediateDER := createAuthority(t, now, "public test intermediate", root, rootKey, nil)
	roots := x509.NewCertPool()
	roots.AddCert(root)

	issue := func(name, hostname string, mutate func(*x509.Certificate)) CertificatePair {
		key := newKey(t)
		template := &x509.Certificate{
			SerialNumber:          nextSerial(),
			Subject:               pkix.Name{CommonName: hostname},
			NotBefore:             now.Add(-time.Hour),
			NotAfter:              now.Add(24 * time.Hour),
			KeyUsage:              x509.KeyUsageDigitalSignature,
			ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			DNSNames:              []string{hostname},
			BasicConstraintsValid: true,
		}
		if mutate != nil {
			mutate(template)
		}
		leafDER := signCertificate(t, template, intermediate, &key.PublicKey, intermediateKey)
		return writePair(t, directory, name, key, leafDER, intermediateDER)
	}
	return DomainConfig{
		Paths: Paths{
			Panel: issue("panel", "panel.example.net", mutatePanel),
			Admin: issue("admin", "admin.example.net", nil),
			Agent: issue("agent", "agent.example.net", nil),
		},
		PanelHost: "panel.example.net", AdminHost: "admin.example.net", AgentHost: "agent.example.net",
		Roots: roots, CurrentTime: now,
	}
}

func newIPFixture(t *testing.T, now time.Time, mutateLeaf, mutateCA func(*x509.Certificate)) IPConfig {
	t.Helper()
	return newIPFixtureAddress(t, now, "203.0.113.10", mutateLeaf, mutateCA)
}

func newIPFixtureAddress(t *testing.T, now time.Time, address string, mutateLeaf, mutateCA func(*x509.Certificate)) IPConfig {
	t.Helper()
	directory := t.TempDir()
	ca, caKey, caDER := createAuthority(t, now, "private test CA", nil, nil, mutateCA)
	key := newKey(t)
	leaf := &x509.Certificate{
		SerialNumber:          nextSerial(),
		Subject:               pkix.Name{CommonName: address},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP(address)},
		BasicConstraintsValid: true,
	}
	if mutateLeaf != nil {
		mutateLeaf(leaf)
	}
	leafDER := signCertificate(t, leaf, ca, &key.PublicKey, caKey)
	pair := writePair(t, directory, "private", key, leafDER, caDER)
	caPath := filepath.Join(directory, "ca.pem")
	writeCertificate(t, caPath, caDER)
	return IPConfig{
		Paths:   Paths{Private: pair, PrivateCertificateAuthority: caPath},
		Address: address, CurrentTime: now,
	}
}

func createAuthority(
	t *testing.T,
	now time.Time,
	commonName string,
	parent *x509.Certificate,
	parentKey *ecdsa.PrivateKey,
	mutate ...func(*x509.Certificate),
) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key := newKey(t)
	template := &x509.Certificate{
		SerialNumber:          nextSerial(),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(48 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	for _, apply := range mutate {
		if apply != nil {
			apply(template)
		}
	}
	if parent == nil {
		parent = template
		parentKey = key
	}
	encoded := signCertificate(t, template, parent, &key.PublicKey, parentKey)
	certificate, err := x509.ParseCertificate(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, key, encoded
}

func signCertificate(t *testing.T, template, parent *x509.Certificate, publicKey *ecdsa.PublicKey, parentKey *ecdsa.PrivateKey) []byte {
	t.Helper()
	encoded, err := x509.CreateCertificate(rand.Reader, template, parent, publicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func writePair(t *testing.T, directory, name string, key *ecdsa.PrivateKey, chain ...[]byte) CertificatePair {
	t.Helper()
	certificatePath := filepath.Join(directory, name+"-fullchain.pem")
	keyPath := filepath.Join(directory, name+"-privkey.pem")
	contents := make([]byte, 0)
	for _, encoded := range chain {
		contents = append(contents, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: encoded})...)
	}
	if err := os.WriteFile(certificatePath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	writePrivateKey(t, keyPath, key)
	return CertificatePair{Certificate: certificatePath, PrivateKey: keyPath}
}

func writePrivateKey(t *testing.T, path string, key *ecdsa.PrivateKey) {
	t.Helper()
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeCertificate(t *testing.T, path string, encoded []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: encoded}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

var serialNumber int64

func nextSerial() *big.Int {
	serialNumber++
	return big.NewInt(serialNumber)
}
