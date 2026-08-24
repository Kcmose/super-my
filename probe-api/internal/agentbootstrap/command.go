package agentbootstrap

import (
	"encoding/base64"
	"strings"
)

const downloadPath = "/downloads/probe-agent"

// Generator renders the administrator-only, one-time Agent bootstrap command.
// Configuration is validated and the optional CA bundle is read at API startup,
// so building a command after a token has been issued cannot fail.
type Generator struct {
	publicURL string
	caBase64  string
}

func New(publicURL string, caPEM []byte) Generator {
	return Generator{
		publicURL: strings.TrimRight(publicURL, "/"),
		caBase64:  base64.StdEncoding.EncodeToString(caPEM),
	}
}

func (generator Generator) Build(enrollmentToken string) string {
	temporaryDirectory := `"$(mktemp -d)"`
	installerPath := `"$probe_agent_install_dir/install.sh"`
	manifestPath := `"$probe_agent_install_dir/SHA256SUMS"`
	caPath := `"$probe_agent_install_dir/private-ca.pem"`

	parts := []string{
		"set -eu",
		"umask 077",
		`[ "$(id -u)" -eq 0 ] || { echo '请使用 root 用户执行此安装命令。' >&2; exit 1; }`,
		"probe_agent_install_dir=" + temporaryDirectory,
		`cleanup() { rm -rf -- "$probe_agent_install_dir"; }`,
		"trap cleanup EXIT HUP INT TERM",
	}
	curlArguments := []string{
		"curl", "-q", "--fail", "--silent", "--show-error", "--proto", shellQuote("=https"), "--tlsv1.2",
		"--connect-timeout", "15", "--max-time", "60",
	}
	installerArguments := []string{
		"sh", installerPath,
		"--api-url", shellQuote(generator.publicURL + "/api/v1/agent"),
		"--download-base-url", shellQuote(generator.publicURL + downloadPath),
		"--enrollment-token-stdin",
	}
	if generator.caBase64 != "" {
		parts = append(parts,
			"command -v base64 >/dev/null 2>&1 || { echo 'base64 is required' >&2; exit 1; }",
			"printf '%s' "+shellQuote(generator.caBase64)+" | base64 -d > "+caPath,
		)
		curlArguments = append(curlArguments, "--cacert", caPath)
		installerArguments = append(installerArguments, "--ca-file", caPath)
	}
	downloadCommand := func(name string, destination string) string {
		arguments := append([]string(nil), curlArguments...)
		arguments = append(arguments,
			shellQuote(generator.publicURL+downloadPath+"/"+name), "--output", destination,
		)
		return strings.Join(arguments, " ")
	}
	parts = append(parts,
		downloadCommand("SHA256SUMS", manifestPath),
		downloadCommand("install.sh", installerPath),
		`probe_agent_installer_hash="$(awk '$2 == "install.sh" { count++; hash = $1 } END { if (count != 1) exit 1; print hash }' "$probe_agent_install_dir/SHA256SUMS")" || { echo 'Agent 安装器校验清单无效。' >&2; exit 1; }`,
		`[ "${#probe_agent_installer_hash}" -eq 64 ] || { echo 'Agent 安装器哈希格式无效。' >&2; exit 1; }`,
		`case "$probe_agent_installer_hash" in *[!0123456789abcdef]*) echo 'Agent 安装器哈希格式无效。' >&2; exit 1 ;; esac`,
		`probe_agent_installer_actual="$(sha256sum "$probe_agent_install_dir/install.sh" | awk '{ print $1 }')"`,
		`[ "$probe_agent_installer_actual" = "$probe_agent_installer_hash" ] || { echo 'Agent 安装器 SHA256 校验失败。' >&2; exit 1; }`,
		`printf '%s\n' `+shellQuote(enrollmentToken)+` | `+strings.Join(installerArguments, " "),
	)

	return "(" + strings.Join(parts, "; ") + ")"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
