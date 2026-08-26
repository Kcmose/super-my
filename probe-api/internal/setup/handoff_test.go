package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadInstalledAccessReconstructsStrictDomainAndIPAdminURLs(t *testing.T) {
	for name, environment := range map[string]string{
		"ip": strings.Join([]string{
			"PROBE_INGRESS_MODE=ip",
			"PROBE_ADMIN_ORIGIN=https://[2001:db8::20]:18455",
			"PROBE_AGENT_PUBLIC_URL=https://[2001:db8::20]:18454",
			"PROBE_AGENT_INSTALL_CA_FILE=" + DefaultPrivateCACertificatePath,
		}, "\n") + "\n",
		"domain": strings.Join([]string{
			"PROBE_INGRESS_MODE=domain",
			"PROBE_ADMIN_ORIGIN=https://admin.monitor.test",
			"PROBE_AGENT_PUBLIC_URL=https://api.monitor.test",
			"PROBE_AGENT_INSTALL_CA_FILE=",
		}, "\n") + "\n",
		"management IP": strings.Join([]string{
			"PROBE_INSTALLATION_PROFILE=management",
			"PROBE_INGRESS_MODE=ip",
			"PROBE_ADMIN_ORIGIN=https://[2001:db8::20]:18455",
			"PROBE_AGENT_PUBLIC_URL=",
			"PROBE_AGENT_INSTALLER_URL=",
			"PROBE_AGENT_INSTALL_CA_FILE=",
		}, "\n") + "\n",
		"management domain": strings.Join([]string{
			"PROBE_INSTALLATION_PROFILE=management",
			"PROBE_INGRESS_MODE=domain",
			"PROBE_ADMIN_ORIGIN=https://admin.monitor.test",
			"PROBE_AGENT_PUBLIC_URL=",
			"PROBE_AGENT_INSTALLER_URL=",
			"PROBE_AGENT_INSTALL_CA_FILE=",
		}, "\n") + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "probe-api.env")
			if err := os.WriteFile(path, []byte(environment), 0o600); err != nil {
				t.Fatal(err)
			}
			access, err := loadInstalledAccess(path)
			if err != nil {
				t.Fatal(err)
			}
			if (name == "ip" || name == "management IP") && (access.Mode != IngressModeIP || access.AdminURL != "https://[2001:db8::20]:18455/login") {
				t.Fatalf("IP access = %#v", access)
			}
			if (name == "domain" || name == "management domain") && (access.Mode != IngressModeDomain || access.AdminURL != "https://admin.monitor.test/login") {
				t.Fatalf("domain access = %#v", access)
			}
		})
	}
}

func TestLoadInstalledAccessRejectsInconsistentOrUnsafeEnvironment(t *testing.T) {
	for name, environment := range map[string]string{
		"wrong CA path":   "PROBE_INGRESS_MODE=ip\nPROBE_ADMIN_ORIGIN=https://192.0.2.10:18455\nPROBE_AGENT_PUBLIC_URL=https://192.0.2.10:18454\nPROBE_AGENT_INSTALL_CA_FILE=/tmp/ca.pem\n",
		"different IPs":   "PROBE_INGRESS_MODE=ip\nPROBE_ADMIN_ORIGIN=https://192.0.2.10:18455\nPROBE_AGENT_PUBLIC_URL=https://192.0.2.11:18454\nPROBE_AGENT_INSTALL_CA_FILE=" + DefaultPrivateCACertificatePath + "\n",
		"credentialed":    "PROBE_INGRESS_MODE=domain\nPROBE_ADMIN_ORIGIN=https://root@admin.monitor.test\nPROBE_AGENT_PUBLIC_URL=https://api.monitor.test\nPROBE_AGENT_INSTALL_CA_FILE=\n",
		"duplicate":       "PROBE_INGRESS_MODE=domain\nPROBE_INGRESS_MODE=ip\nPROBE_ADMIN_ORIGIN=https://admin.monitor.test\nPROBE_AGENT_PUBLIC_URL=https://api.monitor.test\nPROBE_AGENT_INSTALL_CA_FILE=\n",
		"missing CA key":  "PROBE_INGRESS_MODE=domain\nPROBE_ADMIN_ORIGIN=https://admin.monitor.test\nPROBE_AGENT_PUBLIC_URL=https://api.monitor.test\n",
		"unknown profile": "PROBE_INSTALLATION_PROFILE=unknown\nPROBE_INGRESS_MODE=domain\nPROBE_ADMIN_ORIGIN=https://admin.monitor.test\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "probe-api.env")
			if err := os.WriteFile(path, []byte(environment), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadInstalledAccess(path); err == nil {
				t.Fatalf("unsafe environment %q was accepted", name)
			}
		})
	}
}
