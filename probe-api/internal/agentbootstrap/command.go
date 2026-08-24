package agentbootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Generator renders the administrator-only, one-time Agent bootstrap command.
// Configuration is validated and the optional CA bundle is read at API startup,
// so building a command after a token has been issued cannot fail.
type Generator struct {
	publicURL    string
	installerURL string
	caSHA256     string
}

func New(publicURL, installerURL string, caPEM []byte) Generator {
	caSHA256 := ""
	if len(caPEM) > 0 {
		digest := sha256.Sum256(caPEM)
		caSHA256 = hex.EncodeToString(digest[:])
	}
	return Generator{
		publicURL:    strings.TrimRight(publicURL, "/"),
		installerURL: installerURL,
		caSHA256:     caSHA256,
	}
}

func (generator Generator) Build(enrollmentToken string) string {
	download := []string{
		"curl", "-q", "-fsSL", "--proto", shellQuote("=https"), "--proto-redir", shellQuote("=https"),
		"--tlsv1.2", "--connect-timeout", "15", "--max-time", "60", shellQuote(generator.installerURL),
	}
	install := []string{
		"sudo", "bash", "-s", "--", "-e", shellQuote(generator.publicURL), "-t", shellQuote(enrollmentToken),
	}
	if generator.caSHA256 != "" {
		install = append(install, "-c", shellQuote(generator.caSHA256))
	}
	return strings.Join(download, " ") + " | " + strings.Join(install, " ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
