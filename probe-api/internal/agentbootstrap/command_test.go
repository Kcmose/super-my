package agentbootstrap

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildUsesVerifiedHTTPSAndQuotesOneTimeToken(t *testing.T) {
	token := "enroll.v1.one-time-secret"
	installerURL := "https://raw.githubusercontent.com/Kcmose/my-agent/refs/tags/v1.0.1/deploy/install.sh"
	command := New("https://agent.example.com:8443", installerURL, nil).Build(token)

	for _, expected := range []string{
		"curl -q -fsSL", "--proto", "=https", "--proto-redir", "--tlsv1.2",
		"--connect-timeout", "--max-time",
		installerURL, "sudo bash -s --", "-e", "https://agent.example.com:8443", "-t", token,
	} {
		if !strings.Contains(command, expected) {
			t.Fatalf("command does not contain %q: %s", expected, command)
		}
	}
	for _, forbidden := range []string{"base64", "SHA256SUMS", "--enrollment-token-stdin", "--insecure", "curl -k", "eval "} {
		if strings.Contains(command, forbidden) {
			t.Fatalf("command contains unsafe fragment %q: %s", forbidden, command)
		}
	}
	if count := strings.Count(command, token); count != 1 {
		t.Fatalf("one-time token appears %d times in command, want 1: %s", count, command)
	}
	if !strings.HasPrefix(command, "curl -q -fsSL") {
		t.Fatalf("curl must disable per-user configuration before strict TLS flags: %s", command)
	}
}

func TestShellQuoteHandlesSingleQuotes(t *testing.T) {
	if got, want := shellQuote("it's"), `'it'"'"'s'`; got != want {
		t.Fatalf("shellQuote() = %q, want %q", got, want)
	}
}

func TestBuildUsesConfiguredPublicCAFingerprint(t *testing.T) {
	caPEM := []byte("PUBLIC CA\n")
	digest := sha256.Sum256(caPEM)
	command := New("https://192.0.2.10", "https://raw.example/install.sh", caPEM).Build("token")
	if !strings.Contains(command, "-c '"+fmt.Sprintf("%x", digest)+"'") || strings.Contains(command, "PUBLIC CA") {
		t.Fatalf("private-CA fingerprint command is incomplete: %s", command)
	}
}

func TestBuildKeepsPrivateCAPreviewCommandCompact(t *testing.T) {
	installerURL := "https://raw.githubusercontent.com/Kcmose/my-agent/refs/tags/v1.0.1/deploy/install.sh"
	token := "enroll.v1.abcdefghijklmnopqrstuvwxyz01234567890123456"
	command := New("https://192.168.33.253:18454", installerURL, []byte("PUBLIC CA\n")).Build(token)

	if got, limit := len(command), 380; got > limit {
		t.Fatalf("private-CA bootstrap command is %d bytes, want at most %d: %s", got, limit, command)
	}
	if count := strings.Count(command, " | "); count != 1 {
		t.Fatalf("bootstrap command has %d pipes, want exactly 1: %s", count, command)
	}
	if strings.ContainsAny(command, "\r\n") {
		t.Fatalf("bootstrap command must remain one line: %q", command)
	}
}

func TestBuildRunsDownloadedInstallerWithExpectedArguments(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("POSIX shell is unavailable")
	}
	temporaryDirectory := t.TempDir()
	capturePath := filepath.Join(temporaryDirectory, "installer-arguments")
	fakeCurl := `#!/bin/sh
set -eu
printf '%s\n' '#!/bin/sh' 'set -eu' 'printf '\''%s\n'\'' "$@" > "$PROBE_BOOTSTRAP_CAPTURE"'
`
	fakeSudo := `#!/bin/sh
set -eu
[ "$1" = bash ]
shift
exec bash "$@"
`
	for name, contents := range map[string]string{"curl": fakeCurl, "sudo": fakeSudo} {
		path := filepath.Join(temporaryDirectory, name)
		if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	token := "enroll.v1.abcdefghijklmnopqrstuvwxyz0123456789"
	caPEM := []byte("PUBLIC CA\n")
	digest := sha256.Sum256(caPEM)
	command := New("https://agent.example.com:8443", "https://raw.example/install.sh", caPEM).Build(token)
	process := exec.Command("sh", "-c", command)
	process.Env = append(os.Environ(),
		"PATH="+temporaryDirectory+":"+os.Getenv("PATH"),
		"PROBE_BOOTSTRAP_CAPTURE="+capturePath,
	)
	if output, err := process.CombinedOutput(); err != nil {
		t.Fatalf("bootstrap command failed: %v\n%s\ncommand=%s", err, output, command)
	}
	captured, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"-e\nhttps://agent.example.com:8443\n",
		"-t\n" + token + "\n",
		"-c\n" + fmt.Sprintf("%x", digest) + "\n",
	} {
		if !strings.Contains(string(captured), expected) {
			t.Fatalf("installer capture does not contain %q: %q", expected, captured)
		}
	}
}
