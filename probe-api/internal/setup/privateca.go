package setup

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	DefaultPrivateCACertificatePath = "/etc/probe-panel/tls/private-ca/ca.pem"
	maxPrivateCAHandoffBytes        = int64(64 * 1024)
)

type setupPrivateCAMetadata struct {
	Available bool   `json:"available"`
	PEM       string `json:"pem,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

type privateCALoader func(string) (setupPrivateCAMetadata, error)

func validPrivateCAMetadata(metadata setupPrivateCAMetadata) bool {
	if !metadata.Available || len(metadata.PEM) < 1 || int64(len(metadata.PEM)) > maxPrivateCAHandoffBytes || len(metadata.SHA256) != sha256.Size*2 {
		return false
	}
	for _, character := range metadata.SHA256 {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func loadPrivateCA(path string) (setupPrivateCAMetadata, error) {
	contents, err := readStableRegularFile(path, maxPrivateCAHandoffBytes)
	if err != nil {
		return setupPrivateCAMetadata{}, err
	}
	defer clear(contents)
	if err := validatePrivateCAPEM(contents); err != nil {
		return setupPrivateCAMetadata{}, err
	}
	digest := sha256.Sum256(contents)
	return setupPrivateCAMetadata{
		Available: true,
		PEM:       string(contents),
		SHA256:    hex.EncodeToString(digest[:]),
	}, nil
}

func readStableRegularFile(path string, maximum int64) ([]byte, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || maximum < 1 {
		return nil, errors.New("file path or size limit is invalid")
	}
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > maximum {
		return nil, errors.New("file is missing or unsafe")
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("file cannot be opened")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() < 1 || opened.Size() > maximum || !os.SameFile(before, opened) {
		return nil, errors.New("file changed while opening")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || len(contents) < 1 || int64(len(contents)) != opened.Size() || int64(len(contents)) > maximum {
		clear(contents)
		return nil, errors.New("file cannot be read safely")
	}
	after, err := os.Lstat(path)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(opened, after) || after.Size() != opened.Size() {
		clear(contents)
		return nil, errors.New("file changed while reading")
	}
	return contents, nil
}

func validatePrivateCAPEM(contents []byte) error {
	remaining := contents
	certificates := 0
	now := time.Now()
	for {
		remaining = trimPEMWhitespace(remaining)
		if len(remaining) == 0 {
			break
		}
		if !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return errors.New("private CA file contains non-certificate data")
		}
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return errors.New("private CA file contains an invalid PEM block")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !certificate.BasicConstraintsValid || !certificate.IsCA || now.Before(certificate.NotBefore) || now.After(certificate.NotAfter) || (certificate.KeyUsage != 0 && certificate.KeyUsage&x509.KeyUsageCertSign == 0) {
			return errors.New("private CA file contains a certificate that is not a CA")
		}
		certificates++
		remaining = rest
	}
	if certificates == 0 {
		return errors.New("private CA file contains no CA certificate")
	}
	return nil
}

func trimPEMWhitespace(value []byte) []byte {
	for len(value) > 0 {
		switch value[0] {
		case ' ', '\t', '\r', '\n':
			value = value[1:]
		default:
			return value
		}
	}
	return value
}
