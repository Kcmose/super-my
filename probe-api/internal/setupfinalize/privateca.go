package setupfinalize

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"probe-api/internal/setup"
)

func (finalizer *Finalizer) validateAvailableIPPorts(ctx context.Context) error {
	return finalizer.validateAvailableIngressPorts(ctx, "IP ingress", []string{"18453", "18454", "18455"})
}

func (finalizer *Finalizer) validateAvailableIPPortsForProfile(ctx context.Context, profile setup.InstallationProfile) error {
	if profile == setup.InstallationProfileManagement {
		return finalizer.validateAvailableIngressPorts(ctx, "management IP ingress", []string{"18455"})
	}
	return finalizer.validateAvailableIPPorts(ctx)
}

func (finalizer *Finalizer) validateAvailableACMEPorts(ctx context.Context) error {
	return finalizer.validateAvailableIngressPorts(ctx, "ACME", []string{"80", "443"})
}

func (finalizer *Finalizer) validateAvailableIngressPorts(ctx context.Context, purpose string, ports []string) error {
	listeners, err := finalizer.config.Runner.Output(ctx, finalizer.platform.ssPath, "-H", "-lnt")
	if err != nil {
		return fmt.Errorf("inspect %s ports: %w", purpose, err)
	}
	occupied, err := tcpListenerPorts(listeners)
	if err != nil {
		return fmt.Errorf("parse %s port inspection: %w", purpose, err)
	}
	for _, port := range ports {
		if _, exists := occupied[port]; exists {
			return fmt.Errorf("TCP port %s is already in use; refusing to interrupt an unrelated service", port)
		}
	}
	return nil
}

func (finalizer *Finalizer) issuePrivateCertificate(access setup.AccessConfiguration) error {
	if access.Mode != setup.IngressModeIP || !access.Address.IsValid() {
		return errors.New("private certificate requires canonical IP ingress")
	}
	now := finalizer.config.Now().UTC()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return errors.New("generate private CA key")
	}
	caSerial, err := randomSerialNumber()
	if err != nil {
		return err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          caSerial,
		Subject:               pkix.Name{CommonName: "Probe Panel Private CA"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return errors.New("create private CA certificate")
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return errors.New("generate IP service key")
	}
	leafSerial, err := randomSerialNumber()
	if err != nil {
		return err
	}
	leafTemplate := &x509.Certificate{
		SerialNumber:          leafSerial,
		Subject:               pkix.Name{CommonName: access.Address.String()},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.IP(access.Address.AsSlice())},
		BasicConstraintsValid: true,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, &leafKey.PublicKey, caKey)
	if err != nil {
		return errors.New("create IP service certificate")
	}

	caKeyDER, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		return errors.New("encode private CA key")
	}
	leafKeyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return errors.New("encode IP service key")
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	caKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: caKeyDER})
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	leafKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: leafKeyDER})
	fullchainPEM := append(append([]byte(nil), leafPEM...), caPEM...)
	defer clear(caKeyPEM)
	defer clear(leafKeyPEM)

	root := finalizer.config.RootIdentity
	files := []fileSpec{
		{finalizer.config.Paths.PrivateCACertificate, caPEM, 0o644, root},
		{finalizer.config.Paths.PrivateCAKey, caKeyPEM, 0o600, root},
		{finalizer.config.Paths.PrivateCertificate, fullchainPEM, 0o644, root},
		{finalizer.config.Paths.PrivateKey, leafKeyPEM, 0o600, root},
	}
	for _, file := range files {
		if err := createFileAtomic(file); err != nil {
			return err
		}
	}
	if err := validatePrivateCertificate(finalizer.config.Paths, access, now); err != nil {
		return err
	}
	surfaces := []string{"panel", "admin", "api"}
	if access.Profile == setup.InstallationProfileManagement {
		surfaces = []string{"admin"}
	}
	for _, surface := range surfaces {
		if err := createAbsoluteSymlink(finalizer.config.Paths.PrivateCertificate, filepath.Join(finalizer.config.Paths.TLSRoot, surface, "fullchain.pem")); err != nil {
			return err
		}
		if err := createAbsoluteSymlink(finalizer.config.Paths.PrivateKey, filepath.Join(finalizer.config.Paths.TLSRoot, surface, "privkey.pem")); err != nil {
			return err
		}
	}
	return nil
}

func randomSerialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, errors.New("generate certificate serial number")
	}
	// RFC 5280 requires a positive serial. Avoid an otherwise astronomically
	// unlikely installation failure when the CSPRNG returns zero.
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	return serial, nil
}

func validatePrivateCertificate(paths Paths, access setup.AccessConfiguration, now time.Time) error {
	pair, err := tls.LoadX509KeyPair(paths.PrivateCertificate, paths.PrivateKey)
	if err != nil || len(pair.Certificate) < 1 {
		return errors.New("private IP certificate and key are invalid or mismatched")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.IsCA {
		return errors.New("private IP leaf certificate cannot be parsed")
	}
	if err := leaf.VerifyHostname(access.Address.String()); err != nil {
		return errors.New("private IP certificate does not cover the configured address")
	}
	caContents, err := os.ReadFile(paths.PrivateCACertificate)
	if err != nil {
		return errors.New("read generated private CA certificate")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caContents) {
		return errors.New("generated private CA certificate cannot be parsed")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: roots, DNSName: access.Address.String(), CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return fmt.Errorf("verify generated private IP certificate: %w", err)
	}
	return nil
}
