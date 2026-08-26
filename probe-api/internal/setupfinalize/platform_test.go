package setupfinalize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"probe-api/internal/setup"
)

func TestPlatformContractsAndManagementNginxSelection(t *testing.T) {
	tests := []struct {
		platformID     string
		dialect        nginxDialect
		unitProfile    SystemdUnitProfile
		domainTemplate string
		ipTemplate     string
	}{
		{PlatformDebian9Systemd, nginxDialectClassic, SystemdUnitProfileLegacy, "nginx-management-classic.conf", "nginx-management-ip-classic.conf"},
		{PlatformDebian10Systemd, nginxDialectLegacy, SystemdUnitProfileLegacy, "nginx-management-legacy.conf", "nginx-management-ip-legacy.conf"},
		{PlatformDebian11Systemd, nginxDialectLegacy, SystemdUnitProfileLegacy, "nginx-management-legacy.conf", "nginx-management-ip-legacy.conf"},
		{PlatformDebian12Systemd, nginxDialectLegacy, SystemdUnitProfileModern, "nginx-management-legacy.conf", "nginx-management-ip-legacy.conf"},
		{PlatformDebian13Systemd, nginxDialectModern, SystemdUnitProfileModern, "nginx-management.conf", "nginx-management-ip.conf"},
		{PlatformUbuntu1804Systemd, nginxDialectLegacy, SystemdUnitProfileLegacy, "nginx-management-legacy.conf", "nginx-management-ip-legacy.conf"},
		{PlatformUbuntu2004Systemd, nginxDialectLegacy, SystemdUnitProfileLegacy, "nginx-management-legacy.conf", "nginx-management-ip-legacy.conf"},
		{PlatformUbuntu2204Systemd, nginxDialectLegacy, SystemdUnitProfileModern, "nginx-management-legacy.conf", "nginx-management-ip-legacy.conf"},
		{PlatformUbuntu2404Systemd, nginxDialectLegacy, SystemdUnitProfileModern, "nginx-management-legacy.conf", "nginx-management-ip-legacy.conf"},
		{PlatformUbuntu2604Systemd, nginxDialectModern, SystemdUnitProfileModern, "nginx-management.conf", "nginx-management-ip.conf"},
		{PlatformCentOSLinux7Systemd, nginxDialectClassic, SystemdUnitProfileLegacy, "nginx-management-classic.conf", "nginx-management-ip-classic.conf"},
		{PlatformCentOSLinux8Systemd, nginxDialectLegacy, SystemdUnitProfileLegacy, "nginx-management-legacy.conf", "nginx-management-ip-legacy.conf"},
		{PlatformCentOSStream8Systemd, nginxDialectLegacy, SystemdUnitProfileLegacy, "nginx-management-legacy.conf", "nginx-management-ip-legacy.conf"},
		{PlatformCentOSStream9Systemd, nginxDialectLegacy, SystemdUnitProfileModern, "nginx-management-legacy.conf", "nginx-management-ip-legacy.conf"},
		{PlatformCentOSStream10Systemd, nginxDialectModern, SystemdUnitProfileModern, "nginx-management.conf", "nginx-management-ip.conf"},
	}
	canonicalIDs := make([]string, 0, len(tests))

	for _, test := range tests {
		canonicalIDs = append(canonicalIDs, test.platformID)
		t.Run(test.platformID, func(t *testing.T) {
			if err := ValidatePlatformID(test.platformID); err != nil {
				t.Fatalf("ValidatePlatformID(%q): %v", test.platformID, err)
			}
			contract, err := platformContractFor(test.platformID)
			if err != nil || contract.id != test.platformID || contract.nginxDialect != test.dialect || contract.unitProfile != test.unitProfile {
				t.Fatalf("platform contract = %#v, %v", contract, err)
			}
			if profile, err := UnitProfileForPlatformID(test.platformID); err != nil || profile != test.unitProfile {
				t.Fatalf("exported unit profile = %v, %v; want %v", profile, err, test.unitProfile)
			}
			if strings.HasPrefix(test.platformID, "centos-") {
				wantNginxUnits := [2]string{rpmNginxNativeUnit}
				if contract.postgresServiceUnit != rpmPostgresServiceUnit || contract.psqlPath != rpmPsqlPath ||
					contract.pgIsReadyPath != rpmPGIsReadyPath || contract.ssPath != "/usr/sbin/ss" ||
					contract.certbotTimerUnit != rpmCertbotTimerUnit || contract.nginxNativeUnitPaths != wantNginxUnits {
					t.Fatalf("RPM runtime contract = %#v", contract)
				}
				if contract.setprivSupportsAmbientCaps != (test.platformID != PlatformCentOSLinux7Systemd) {
					t.Fatalf("RPM ambient-capability contract = %v", contract.setprivSupportsAmbientCaps)
				}
			} else {
				wantNginxUnits := [2]string{debianNginxNativeUnit, debianNginxNativeUnitAlt}
				if contract.postgresServiceUnit != debianPostgresServiceUnit || contract.psqlPath != debianPsqlPath ||
					contract.pgIsReadyPath != debianPGIsReadyPath || contract.ssPath != "/bin/ss" ||
					contract.certbotTimerUnit != debianCertbotTimerUnit ||
					contract.nginxNativeUnitPaths != wantNginxUnits || !contract.setprivSupportsAmbientCaps {
					t.Fatalf("Deb runtime contract = %#v", contract)
				}
			}

			domainAccess := setup.AccessConfiguration{
				Mode: setup.IngressModeDomain, Profile: setup.InstallationProfileManagement,
			}
			ipAccess := setup.AccessConfiguration{
				Mode: setup.IngressModeIP, Profile: setup.InstallationProfileManagement,
			}
			if got := nginxTemplateFor(domainAccess, contract); got != test.domainTemplate {
				t.Fatalf("domain template = %q; want %q", got, test.domainTemplate)
			}
			if got := nginxTemplateFor(ipAccess, contract); got != test.ipTemplate {
				t.Fatalf("IP template = %q; want %q", got, test.ipTemplate)
			}
			if got := managementNginxTemplateNames(contract); len(got) != 2 || got[0] != test.domainTemplate || got[1] != test.ipTemplate {
				t.Fatalf("required management templates = %q", got)
			}

			base := t.TempDir()
			finalizer, err := New(Config{
				BundleRoot: filepath.Join(base, "bundle"), ReleaseID: "v1.2.0",
				PlatformID: test.platformID, Paths: testPaths(base),
				CommitInstalled: func(time.Time) error { return nil },
			})
			if err != nil || finalizer.platform != contract {
				t.Fatalf("New platform = %#v, %v", finalizer, err)
			}
		})
	}
	if got := strings.Join(canonicalIDs, ","); got != SupportedPlatformIDs {
		t.Fatalf("canonical platform order = %q; want %q", got, SupportedPlatformIDs)
	}

	for _, platformID := range []string{"", "unknown-systemd", "debian-13-systemd ", " debian-13-systemd"} {
		t.Run("reject_"+platformID, func(t *testing.T) {
			if err := ValidatePlatformID(platformID); err == nil {
				t.Fatalf("unsupported platform %q was accepted", platformID)
			}
			if profile, err := UnitProfileForPlatformID(platformID); err == nil || profile != SystemdUnitProfileInvalid {
				t.Fatalf("unsupported platform %q exposed unit profile %v, %v", platformID, profile, err)
			}
			base := t.TempDir()
			if _, err := New(Config{
				BundleRoot: filepath.Join(base, "bundle"), ReleaseID: "v1.2.0",
				PlatformID: platformID, Paths: testPaths(base),
				CommitInstalled: func(time.Time) error { return nil },
			}); err == nil {
				t.Fatalf("New accepted unsupported platform %q", platformID)
			}
		})
	}

	invalidContract := platformContract{}
	managementAccess := setup.AccessConfiguration{
		Mode: setup.IngressModeDomain, Profile: setup.InstallationProfileManagement,
	}
	if template := nginxTemplateFor(managementAccess, invalidContract); template != "" {
		t.Fatalf("invalid platform selected management template %q", template)
	}
	if templates := managementNginxTemplateNames(invalidContract); len(templates) != 0 {
		t.Fatalf("invalid platform required management templates %q", templates)
	}
	if _, ok := systemdUnitSourcesFor(invalidContract); ok {
		t.Fatal("invalid platform selected systemd unit sources")
	}
}

func TestManagementBundleRequiresCompleteRuntimeTemplateSet(t *testing.T) {
	templates := []string{
		"nginx-management.conf",
		"nginx-management-ip.conf",
		"nginx-management-legacy.conf",
		"nginx-management-ip-legacy.conf",
		"nginx-management-classic.conf",
		"nginx-management-ip-classic.conf",
	}
	access := setup.AccessConfiguration{
		Mode: setup.IngressModeDomain, Profile: setup.InstallationProfileManagement,
	}

	for _, platformID := range strings.Split(SupportedPlatformIDs, ",") {
		t.Run(platformID, func(t *testing.T) {
			for _, missingTemplate := range templates {
				t.Run(missingTemplate, func(t *testing.T) {
					base := t.TempDir()
					bundle := filepath.Join(base, "bundle")
					writeTestBundle(t, bundle)
					finalizer, err := New(Config{
						BundleRoot: bundle, ReleaseID: "v1.2.0", PlatformID: platformID,
						Paths: testPaths(base), CommitInstalled: func(time.Time) error { return nil },
					})
					if err != nil {
						t.Fatal(err)
					}
					if err := finalizer.validateBundle(access); err != nil {
						t.Fatalf("complete bundle was rejected: %v", err)
					}

					missingPath := filepath.Join(bundle, "source", "probe-api", "deploy", "nginx", missingTemplate)
					if err := os.Remove(missingPath); err != nil {
						t.Fatal(err)
					}
					if err := finalizer.validateBundle(access); err == nil || !strings.Contains(err.Error(), missingTemplate) {
						t.Fatalf("bundle without %s error = %v", missingTemplate, err)
					}
				})
			}
		})
	}
}

func TestManagementBundleRequiresSelectedSystemdUnitProfile(t *testing.T) {
	access := setup.AccessConfiguration{
		Mode: setup.IngressModeDomain, Profile: setup.InstallationProfileManagement,
	}
	for _, platformID := range strings.Split(SupportedPlatformIDs, ",") {
		t.Run(platformID, func(t *testing.T) {
			contract, err := platformContractFor(platformID)
			if err != nil {
				t.Fatal(err)
			}
			sources, ok := systemdUnitSourcesFor(contract)
			if !ok {
				t.Fatal("platform has no unit sources")
			}
			for _, missingSource := range sources.requiredBundlePaths() {
				t.Run(filepath.Base(missingSource), func(t *testing.T) {
					base := t.TempDir()
					bundle := filepath.Join(base, "bundle")
					writeTestBundle(t, bundle)
					finalizer, newErr := New(Config{
						BundleRoot: bundle, ReleaseID: "v1.2.0", PlatformID: platformID,
						Paths: testPaths(base), CommitInstalled: func(time.Time) error { return nil },
					})
					if newErr != nil {
						t.Fatal(newErr)
					}
					if err := finalizer.validateBundle(access); err != nil {
						t.Fatalf("complete bundle was rejected: %v", err)
					}

					missingPath := filepath.Join(bundle, "source", "probe-api", filepath.FromSlash(missingSource))
					if err := os.Remove(missingPath); err != nil {
						t.Fatal(err)
					}
					if err := finalizer.validateBundle(access); err == nil || !strings.Contains(err.Error(), filepath.Base(missingSource)) {
						t.Fatalf("bundle without %s error = %v", missingSource, err)
					}
				})
			}
		})
	}
}

func TestLegacySystemdAssetsKeepTheSystemd219SecurityBaseline(t *testing.T) {
	legacyFiles := []string{
		filepath.Join("..", "..", "deploy", "setup", "probe-panel-setup-legacy.service"),
		filepath.Join("..", "..", "deploy", "setup", "probe-panel-setup-legacy.socket"),
		filepath.Join("..", "..", "deploy", "setup", "probe-panel-finalizer-management-legacy.service"),
		filepath.Join("..", "..", "deploy", "setup", "probe-panel-finalizer-legacy.service"),
		filepath.Join("..", "..", "deploy", "systemd", "probe-api-legacy.service"),
		filepath.Join("..", "..", "deploy", "systemd", "probe-postgres-backup-legacy.service"),
		filepath.Join("..", "..", "deploy", "systemd", "probe-postgres-backup-legacy.timer"),
	}
	forbidden := []string{
		"AmbientCapabilities=", "ProtectClock=", "ProtectControlGroups=", "ProtectHostname=",
		"ProtectKernelLogs=", "ProtectKernelModules=", "ProtectKernelTunables=", "ProtectProc=",
		"ProtectSystem=strict", "RestrictNamespaces=", "RestrictRealtime=", "RestrictSUIDSGID=",
		"LockPersonality=", "MemoryDenyWriteExecute=", "SystemCallFilter=", "SystemCallErrorNumber=",
		"SocketBindAllow=", "SocketBindDeny=", "RuntimeDirectory=", "RuntimeDirectoryMode=", "StateDirectory=",
		"RandomizedDelaySec=", "FileDescriptorName=",
	}
	for _, path := range legacyFiles {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, directive := range forbidden {
			if strings.Contains(string(contents), directive) {
				t.Fatalf("legacy asset %s contains post-systemd-219 directive %q", filepath.Base(path), directive)
			}
		}
	}

	setupUnit := readDeployAsset(t, "setup", "probe-panel-setup-legacy.service")
	for _, required := range []string{
		"CapabilityBoundingSet=\n", "PrivateNetwork=true\n", "ProtectSystem=full\n",
		"ReadOnlyPaths=/srv/probe/setup-ui\n", "ReadWritePaths=/run/probe-panel-setup\n",
		"RestrictAddressFamilies=AF_UNIX\n",
	} {
		if !strings.Contains(setupUnit, required) {
			t.Fatalf("legacy setup unit is missing %q", strings.TrimSpace(required))
		}
	}
	for name, readOnlyPath := range map[string]string{
		"probe-api-legacy.service":             "ReadOnlyPaths=/srv/probe\n",
		"probe-postgres-backup-legacy.service": "ReadOnlyPaths=/srv/probe/config\n",
	} {
		contents := readDeployAsset(t, "systemd", name)
		for _, required := range []string{"CapabilityBoundingSet=\n", "ProtectSystem=full\n", readOnlyPath, "RestrictAddressFamilies="} {
			if !strings.Contains(contents, required) {
				t.Fatalf("legacy runtime unit %s is missing %q", name, strings.TrimSpace(required))
			}
		}
	}
	finalizer := readDeployAsset(t, "setup", "probe-panel-finalizer-management-legacy.service")
	for _, required := range []string{
		"CapabilityBoundingSet=CAP_CHOWN CAP_DAC_OVERRIDE CAP_FOWNER CAP_NET_BIND_SERVICE CAP_SETGID CAP_SETUID\n",
		"ProtectSystem=full\n", "ReadOnlyPaths=/srv/probe/setup-ui\n", "ReadWritePaths=/etc/systemd/system\n",
		"RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK\n",
	} {
		if !strings.Contains(finalizer, required) {
			t.Fatalf("legacy finalizer unit is missing %q", strings.TrimSpace(required))
		}
	}
}

func readDeployAsset(t *testing.T, directory, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "deploy", directory, name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(contents), "\r\n", "\n")
}

func TestManagementBundleRequiresGeneratedDeployRuntime(t *testing.T) {
	base := t.TempDir()
	bundle := filepath.Join(base, "bundle")
	writeTestBundle(t, bundle)
	finalizer, err := New(Config{
		BundleRoot: bundle, ReleaseID: "v1.2.0", PlatformID: PlatformDebian13Systemd,
		Paths: testPaths(base), CommitInstalled: func(time.Time) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	access := setup.AccessConfiguration{
		Mode: setup.IngressModeDomain, Profile: setup.InstallationProfileManagement,
	}
	deployCommon := filepath.Join(bundle, "source", "probe-api", "deploy", "scripts", "deploy-common.sh")
	if err := os.Remove(deployCommon); err != nil {
		t.Fatal(err)
	}
	if err := finalizer.validateBundle(access); err == nil || !strings.Contains(err.Error(), "deploy-common.sh") {
		t.Fatalf("bundle without generated deploy runtime error = %v", err)
	}
}

func TestManagementBundleManifestMustMatchRuntimeAndCanonicalPlatformAllowlist(t *testing.T) {
	for name, manifest := range map[string]string{
		"wrong runtime ABI": "runtime_abi=unknown-v1\nplatform_ids=" + SupportedPlatformIDs + "\n",
		"reordered platforms": "runtime_abi=" + RuntimeABISystemdV2 + "\nplatform_ids=" + strings.Join([]string{
			PlatformDebian10Systemd, PlatformDebian9Systemd, PlatformDebian11Systemd, PlatformDebian12Systemd, PlatformDebian13Systemd,
			PlatformUbuntu1804Systemd, PlatformUbuntu2004Systemd, PlatformUbuntu2204Systemd, PlatformUbuntu2404Systemd, PlatformUbuntu2604Systemd,
			PlatformCentOSLinux7Systemd, PlatformCentOSLinux8Systemd, PlatformCentOSStream8Systemd, PlatformCentOSStream9Systemd, PlatformCentOSStream10Systemd,
		}, ",") + "\n",
		"platform subset": "runtime_abi=" + RuntimeABISystemdV2 + "\nplatform_ids=" + strings.Join([]string{
			PlatformDebian9Systemd, PlatformDebian10Systemd, PlatformDebian11Systemd, PlatformDebian12Systemd, PlatformDebian13Systemd,
		}, ",") + "\n",
		"platform superset":   "runtime_abi=" + RuntimeABISystemdV2 + "\nplatform_ids=" + SupportedPlatformIDs + ",debian-14-systemd\n",
		"duplicate platform":  "runtime_abi=" + RuntimeABISystemdV2 + "\nplatform_ids=" + SupportedPlatformIDs + "," + PlatformDebian13Systemd + "\n",
		"platform whitespace": "runtime_abi=" + RuntimeABISystemdV2 + "\nplatform_ids=" + strings.Replace(SupportedPlatformIDs, ",", ", ", 1) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			bundle := filepath.Join(base, "bundle")
			writeTestBundle(t, bundle)
			if err := os.WriteFile(filepath.Join(bundle, "RELEASE-MANIFEST"), []byte(manifest), 0o755); err != nil {
				t.Fatal(err)
			}
			finalizer, err := New(Config{
				BundleRoot: bundle, ReleaseID: "v1.2.0", PlatformID: PlatformDebian13Systemd,
				Paths: testPaths(base), CommitInstalled: func(time.Time) error { return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			access := setup.AccessConfiguration{
				Mode: setup.IngressModeDomain, Profile: setup.InstallationProfileManagement,
			}
			if err := finalizer.validateBundle(access); err == nil {
				t.Fatalf("incompatible release manifest was accepted: %q", manifest)
			}
		})
	}
}

func TestLegacyManagementNginxTemplatesOnlyChangeHTTP2Dialect(t *testing.T) {
	domainModern := readNginxTemplate(t, "nginx-management.conf")
	domainLegacy := readNginxTemplate(t, "nginx-management-legacy.conf")
	ipModern := readNginxTemplate(t, "nginx-management-ip.conf")
	ipLegacy := readNginxTemplate(t, "nginx-management-ip-legacy.conf")

	for _, required := range []string{"    listen 443 ssl http2;\n", "    listen [::]:443 ssl http2;\n"} {
		if strings.Count(domainLegacy, required) != 1 {
			t.Fatalf("legacy domain template listener %q count is not one", strings.TrimSpace(required))
		}
	}
	for _, required := range []string{"    listen 18455 ssl http2 default_server;\n", "    listen [::]:18455 ssl http2 default_server;\n"} {
		if strings.Count(ipLegacy, required) != 1 {
			t.Fatalf("legacy IP template listener %q count is not one", strings.TrimSpace(required))
		}
	}
	if strings.Contains(domainLegacy, "http2 on;") || strings.Contains(ipLegacy, "http2 on;") {
		t.Fatal("legacy management templates contain the modern standalone HTTP/2 directive")
	}
	if !strings.Contains(domainModern, "    http2 on;\n") || !strings.Contains(ipModern, "    http2 on;\n") {
		t.Fatal("modern management templates lost the standalone HTTP/2 directive")
	}
	if normalizeManagementNginxDialect(domainModern) != normalizeManagementNginxDialect(domainLegacy) {
		t.Fatal("legacy domain template changed routing or security content")
	}
	if normalizeManagementNginxDialect(ipModern) != normalizeManagementNginxDialect(ipLegacy) {
		t.Fatal("legacy IP template changed routing or security content")
	}
}

func TestClassicManagementNginxTemplatesUseOldestReviewedNginxVocabulary(t *testing.T) {
	domainLegacy := readNginxTemplate(t, "nginx-management-legacy.conf")
	domainClassic := readNginxTemplate(t, "nginx-management-classic.conf")
	ipLegacy := readNginxTemplate(t, "nginx-management-ip-legacy.conf")
	ipClassic := readNginxTemplate(t, "nginx-management-ip-classic.conf")

	for name, contents := range map[string]string{"domain": domainClassic, "IP": ipClassic} {
		if strings.Contains(contents, "TLSv1.3") || strings.Contains(contents, "$request_id") {
			t.Fatalf("classic %s template uses an unsupported Nginx/OpenSSL token", name)
		}
		if !strings.Contains(contents, "ssl_protocols TLSv1.2;") {
			t.Fatalf("classic %s template lost the TLS 1.2 baseline", name)
		}
	}
	if normalizeClassicManagementNginx(domainClassic) != domainLegacy {
		t.Fatal("classic domain template changed routing or security content beyond its compatibility vocabulary")
	}
	if normalizeClassicManagementNginx(ipClassic) != ipLegacy {
		t.Fatal("classic IP template changed routing or security content beyond its compatibility vocabulary")
	}
}

func readNginxTemplate(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "deploy", "nginx", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(contents), "\r\n", "\n")
}

func normalizeManagementNginxDialect(contents string) string {
	replacer := strings.NewReplacer(
		"    listen 443 ssl;\n", "    listen 443 ssl http2;\n",
		"    listen [::]:443 ssl;\n", "    listen [::]:443 ssl http2;\n",
		"    listen 18455 ssl default_server;\n", "    listen 18455 ssl http2 default_server;\n",
		"    listen [::]:18455 ssl default_server;\n", "    listen [::]:18455 ssl http2 default_server;\n",
		"    http2 on;\n", "",
	)
	return replacer.Replace(contents)
}

func normalizeClassicManagementNginx(contents string) string {
	replacer := strings.NewReplacer(
		"    ssl_protocols TLSv1.2;\n", "    ssl_protocols TLSv1.2 TLSv1.3;\n",
		`"message":"too many requests; try again later"}'`, `"message":"too many requests; try again later","request_id":"$request_id"}'`,
	)
	return replacer.Replace(contents)
}
