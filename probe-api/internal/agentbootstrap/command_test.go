package agentbootstrap

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildUsesVerifiedHTTPSAndQuotesOneTimeToken(t *testing.T) {
	token := "enroll.v1.one-time-secret"
	command := New("https://agent.example.com:8443", nil).Build(token)

	for _, expected := range []string{
		"--proto", "=https", "--tlsv1.2",
		"--connect-timeout", "--max-time",
		"https://agent.example.com:8443/downloads/probe-agent/SHA256SUMS",
		"https://agent.example.com:8443/downloads/probe-agent/install.sh",
		"--api-url", "https://agent.example.com:8443/api/v1/agent",
		"--download-base-url", "https://agent.example.com:8443/downloads/probe-agent",
		"--enrollment-token-stdin", token,
		"mktemp -d", "trap cleanup", "sha256sum", "id -u",
	} {
		if !strings.Contains(command, expected) {
			t.Fatalf("command does not contain %q: %s", expected, command)
		}
	}
	for _, forbidden := range []string{"sh -c ", "sudo", "curl -k", "--insecure", "curl |", "eval "} {
		if strings.Contains(command, forbidden) {
			t.Fatalf("command contains unsafe fragment %q: %s", forbidden, command)
		}
	}
	if count := strings.Count(command, token); count != 1 {
		t.Fatalf("one-time token appears %d times in command, want 1: %s", count, command)
	}
	if !strings.Contains(command, "curl -q --fail") {
		t.Fatalf("curl must disable per-user configuration before strict TLS flags: %s", command)
	}
}

func TestShellQuoteHandlesSingleQuotes(t *testing.T) {
	if got, want := shellQuote("it's"), `'it'"'"'s'`; got != want {
		t.Fatalf("shellQuote() = %q, want %q", got, want)
	}
}

func TestBuildEmbedsConfiguredPublicCA(t *testing.T) {
	command := New("https://192.0.2.10", []byte("PUBLIC CA\n")).Build("token")
	if !strings.Contains(command, "UFVCTElDIENBCg==") ||
		!strings.Contains(command, "base64 -d") ||
		!strings.Contains(command, "--cacert") ||
		!strings.Contains(command, "--ca-file") {
		t.Fatalf("private-CA command is incomplete: %s", command)
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
output=
while [ "$#" -gt 0 ]; do
	if [ "$1" = --output ]; then
		output=$2
		shift 2
	else
		shift
	fi
done
[ -n "$output" ]
write_installer() {
	printf '%s\n' \
		'#!/bin/sh' \
		'set -eu' \
		'printf '\''%s\n'\'' "$@" > "$PROBE_BOOTSTRAP_CAPTURE"' \
		'while [ "$#" -gt 0 ]; do if [ "$1" = --ca-file ]; then test -s "$2"; cat "$2" >> "$PROBE_BOOTSTRAP_CAPTURE"; shift 2; elif [ "$1" = --enrollment-token-stdin ]; then IFS= read -r token; printf '\''stdin-token\n%s\n'\'' "$token" >> "$PROBE_BOOTSTRAP_CAPTURE"; shift; else shift; fi; done' \
		> "$1"
}
case "$output" in
	*/SHA256SUMS)
		fixture="${output}.installer"
		write_installer "$fixture"
		hash=$(sha256sum "$fixture" | awk '{ print $1 }')
		printf '%s  install.sh\n' "$hash" > "$output"
		rm -f "$fixture"
		;;
	*) write_installer "$output" ;;
esac
`
	fakeID := "#!/bin/sh\n[ \"${1:-}\" = -u ] && printf '0\\n'\n"
	for name, contents := range map[string]string{"curl": fakeCurl, "id": fakeID} {
		path := filepath.Join(temporaryDirectory, name)
		if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	token := "enroll.v1.abcdefghijklmnopqrstuvwxyz0123456789"
	command := New("https://agent.example.com:8443", []byte("PUBLIC CA\n")).Build(token)
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
		"--api-url\nhttps://agent.example.com:8443/api/v1/agent\n",
		"--download-base-url\nhttps://agent.example.com:8443/downloads/probe-agent\n",
		"--enrollment-token-stdin\n",
		"stdin-token\n" + token + "\n",
		"--ca-file\n",
		"PUBLIC CA\n",
	} {
		if !strings.Contains(string(captured), expected) {
			t.Fatalf("installer capture does not contain %q: %q", expected, captured)
		}
	}
}
