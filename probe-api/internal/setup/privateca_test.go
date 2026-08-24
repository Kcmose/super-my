package setup

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadPrivateCAHashesTheExactValidatedPEMFile(t *testing.T) {
	contents := testCertificatePEM(t, true)
	contents = append([]byte("\n"), contents...)
	contents = append(contents, '\n')
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	metadata, err := loadPrivateCA(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	if !metadata.Available || metadata.PEM != string(contents) || metadata.SHA256 != hex.EncodeToString(digest[:]) || len(metadata.SHA256) != 64 || metadata.SHA256 != strings.ToLower(metadata.SHA256) {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestLoadPrivateCARejectsUnsafeOrNonCAContents(t *testing.T) {
	directory := t.TempDir()
	valid := testCertificatePEM(t, true)
	validPath := filepath.Join(directory, "valid.pem")
	if err := os.WriteFile(validPath, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(directory, "linked.pem")
	if err := os.Symlink(validPath, symlinkPath); err == nil {
		if _, err := loadPrivateCA(symlinkPath); err == nil {
			t.Fatal("private CA symlink was accepted")
		}
	}

	for name, contents := range map[string][]byte{
		"empty":              {},
		"non CA certificate": testCertificatePEM(t, false),
		"prefixed text":      append([]byte("not pem\n"), valid...),
		"private key":        pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1, 2, 3}}),
		"oversized":          bytesOf('A', int(maxPrivateCAHandoffBytes)+1),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(directory, strings.ReplaceAll(name, " ", "-")+".pem")
			if err := os.WriteFile(path, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadPrivateCA(path); err == nil {
				t.Fatalf("unsafe private CA %q was accepted", name)
			}
		})
	}
}

func testCertificatePEM(t *testing.T, isCA bool) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Probe setup test"},
		NotBefore:             time.Unix(0, 0),
		NotAfter:              time.Unix(4102444800, 0),
		BasicConstraintsValid: true,
		IsCA:                  isCA,
		KeyUsage:              x509.KeyUsageDigitalSignature,
	}
	if isCA {
		template.KeyUsage |= x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func bytesOf(value byte, count int) []byte {
	contents := make([]byte, count)
	for index := range contents {
		contents[index] = value
	}
	return contents
}
