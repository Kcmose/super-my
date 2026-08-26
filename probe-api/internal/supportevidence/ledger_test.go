package supportevidence

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type supportFixture struct {
	root                    string
	release                 string
	policy                  Policy
	ledger                  ReleaseLedger
	assetsDir               string
	sourceCommit            string
	assets                  map[string]trustedAsset
	upgradeFromRelease      string
	upgradeFromAssetsDir    string
	upgradeFromSourceCommit string
	upgradeFromAssets       map[string]trustedAsset
}

func TestRepositoryLedgerStartsWithSixtyCandidates(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate the repository fixture")
	}
	root := filepath.Join(filepath.Dir(filename), "..", "..", "deploy", "support")
	summary, err := VerifyDirectory(root, "v1.2.0", VerifyOptions{RequireZeroSupported: true})
	if err != nil {
		t.Fatalf("repository support ledger is invalid: %v", err)
	}
	if summary.Cells != 60 || summary.Candidate != 60 || summary.Supported != 0 || summary.SupportedPlatforms != 0 {
		t.Fatalf("unexpected initial summary: %+v", summary)
	}
}

func TestCandidateMatrixIsExactAndCanonical(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ReleaseLedger)
	}{
		{
			name: "missing cell",
			mutate: func(ledger *ReleaseLedger) {
				ledger.Cells = ledger.Cells[:len(ledger.Cells)-1]
			},
		},
		{
			name: "duplicate cell",
			mutate: func(ledger *ReleaseLedger) {
				ledger.Cells[len(ledger.Cells)-1] = ledger.Cells[0]
			},
		},
		{
			name: "reordered cells",
			mutate: func(ledger *ReleaseLedger) {
				ledger.Cells[0], ledger.Cells[1] = ledger.Cells[1], ledger.Cells[0]
			},
		},
		{
			name: "extra platform",
			mutate: func(ledger *ReleaseLedger) {
				ledger.Cells[len(ledger.Cells)-1].PlatformID = "rocky-9-systemd"
			},
		},
		{
			name: "missing promotion eligibility",
			mutate: func(ledger *ReleaseLedger) {
				ledger.PromotionEligible = nil
			},
		},
		{
			name: "contradictory promotion eligibility",
			mutate: func(ledger *ReleaseLedger) {
				eligible := false
				ledger.PromotionEligible = &eligible
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSupportFixture(t)
			test.mutate(&fixture.ledger)
			fixture.writeLedger(t)
			if _, err := fixture.verify(false); err == nil {
				t.Fatal("invalid matrix was accepted")
			}
		})
	}
}

func TestStrictJSONRejectsAmbiguity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{
			name: "unknown field",
			mutate: func(data []byte) []byte {
				return bytes.Replace(data, []byte("{"), []byte("{\n  \"unexpected\": true,"), 1)
			},
		},
		{
			name: "duplicate field",
			mutate: func(data []byte) []byte {
				return bytes.Replace(data, []byte("{"), []byte("{\n  \"schema\": \"weakened\","), 1)
			},
		},
		{
			name: "trailing value",
			mutate: func(data []byte) []byte {
				return append(data, []byte("\n{}\n")...)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSupportFixture(t)
			policyPath := filepath.Join(fixture.root, "policy-v1.json")
			data, err := os.ReadFile(policyPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(policyPath, test.mutate(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.verify(false); err == nil {
				t.Fatal("ambiguous JSON was accepted")
			}
		})
	}
}

func TestPinnedRepositoryAndPromotionLineageCannotBeWeakened(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Policy)
	}{
		{
			name: "source repository",
			mutate: func(policy *Policy) {
				policy.SourceRepository = "someone/else"
			},
		},
		{
			name: "first promotable release",
			mutate: func(policy *Policy) {
				policy.FirstPromotableRelease = "v1.2.0"
			},
		},
		{
			name: "promotion predecessor",
			mutate: func(policy *Policy) {
				policy.PromotionLineage[0].UpgradeFromRelease = "v1.1.0"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSupportFixture(t)
			test.mutate(&fixture.policy)
			writeJSON(t, filepath.Join(fixture.root, "policy-v1.json"), fixture.policy)
			if _, err := fixture.verify(false); err == nil || !strings.Contains(err.Error(), "pinned formal-support contract") {
				t.Fatalf("weakened policy was accepted: %v", err)
			}
		})
	}
}

func TestSupportedCellRequiresCompleteEvidence(t *testing.T) {
	fixture := newSupportFixture(t)
	fixture.ledger.Cells[8].Claim = claimSupported
	fixture.writeLedger(t)
	if _, err := fixture.verify(false); err == nil || !strings.Contains(err.Error(), "has no evidence suite") {
		t.Fatalf("supported claim without evidence was not rejected: %v", err)
	}
}

func TestV120CandidateBaselineCannotBePromoted(t *testing.T) {
	fixture := newSupportFixtureForRelease(t, "v1.2.0")
	fixture.supportCell(t, 8, nil)
	if _, err := fixture.verify(false); err == nil || !strings.Contains(err.Error(), "not promotion eligible") {
		t.Fatalf("v1.2.0 baseline accepted a supported claim: %v", err)
	}
}

func TestV120PromotionEligibilityCannotBeChangedToTrue(t *testing.T) {
	fixture := newSupportFixtureForRelease(t, "v1.2.0")
	eligible := true
	fixture.ledger.PromotionEligible = &eligible
	fixture.writeLedger(t)
	if _, err := fixture.verify(false); err == nil || !strings.Contains(err.Error(), "promotion_eligible must be false") {
		t.Fatalf("v1.2.0 accepted promotion_eligible=true: %v", err)
	}
}

func TestUnknownReleaseFailsClosed(t *testing.T) {
	fixture := newSupportFixtureForRelease(t, "v1.2.2")
	if _, err := fixture.verify(false); err == nil || !strings.Contains(err.Error(), "not present in the pinned promotion lineage") {
		t.Fatalf("unknown release did not fail closed: %v", err)
	}
}

func TestReleaseMustUseCanonicalSemver(t *testing.T) {
	fixture := newSupportFixture(t)
	if _, err := VerifyDirectory(fixture.root, "v01.2.1", VerifyOptions{}); err == nil || !strings.Contains(err.Error(), "exact vMAJOR.MINOR.PATCH") {
		t.Fatalf("release with a leading zero was accepted: %v", err)
	}
}

func TestTrustedReleaseSubjectRejectsCoordinatedLedgerAndEvidenceTampering(t *testing.T) {
	fixture := newSupportFixture(t)
	fixture.supportCell(t, 16, nil)
	claim := &fixture.ledger.Cells[16]
	evidencePath := filepath.Join(fixture.root, filepath.FromSlash(claim.Evidence))
	data, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	var suite EvidenceSuite
	if err := json.Unmarshal(data, &suite); err != nil {
		t.Fatal(err)
	}
	claim.AssetSHA256 = strings.Repeat("e", 64)
	claim.BundleManifestSHA256 = strings.Repeat("f", 64)
	suite.Subject.AssetSHA256 = claim.AssetSHA256
	suite.Subject.BundleManifestSHA256 = claim.BundleManifestSHA256
	data, err = json.MarshalIndent(suite, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(evidencePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	claim.EvidenceSHA256 = hex.EncodeToString(digest[:])
	fixture.writeLedger(t)
	if _, err := fixture.verify(false); err == nil || !strings.Contains(err.Error(), "trusted release asset") {
		t.Fatalf("coordinated subject tampering was not rejected by actual assets: %v", err)
	}
}

func TestTrustedPredecessorRejectsEvidenceTampering(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*EvidenceSubject)
	}{
		{
			name: "source commit",
			mutate: func(subject *EvidenceSubject) {
				subject.UpgradeFromSourceCommit = strings.Repeat("e", 40)
			},
		},
		{
			name: "asset hash",
			mutate: func(subject *EvidenceSubject) {
				subject.UpgradeFromAssetSHA256 = strings.Repeat("e", 64)
			},
		},
		{
			name: "bundle manifest hash",
			mutate: func(subject *EvidenceSubject) {
				subject.UpgradeFromBundleManifestSHA256 = strings.Repeat("e", 64)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSupportFixture(t)
			fixture.supportCell(t, 16, func(suite *EvidenceSuite) {
				test.mutate(&suite.Subject)
			})
			if _, err := fixture.verify(false); err == nil || !strings.Contains(err.Error(), "trusted predecessor release") {
				t.Fatalf("tampered predecessor evidence was accepted: %v", err)
			}
		})
	}
}

func TestTrustedPredecessorCommitMustMatchItsReleaseManifest(t *testing.T) {
	fixture := newSupportFixture(t)
	_, err := VerifyDirectory(fixture.root, fixture.release, VerifyOptions{
		ReleaseAssetsDir:        fixture.assetsDir,
		SourceCommit:            fixture.sourceCommit,
		UpgradeFromAssetsDir:    fixture.upgradeFromAssetsDir,
		UpgradeFromSourceCommit: strings.Repeat("e", 40),
	})
	if err == nil || !strings.Contains(err.Error(), "source_commit must be") {
		t.Fatalf("predecessor commit inconsistent with RELEASE-MANIFEST was accepted: %v", err)
	}
}

func TestMaintainedIPCellCanBeFormallySupported(t *testing.T) {
	fixture := newSupportFixture(t)
	fixture.supportCell(t, 16, nil)
	summary, err := fixture.verify(false)
	if err != nil {
		t.Fatalf("valid supported evidence was rejected: %v", err)
	}
	if summary.Supported != 1 || summary.Candidate != 59 || summary.SupportedPlatforms != 0 {
		t.Fatalf("partial cell support was rolled up incorrectly: %+v", summary)
	}
	if _, err := fixture.verify(true); err == nil {
		t.Fatal("require-zero-supported accepted a supported cell")
	}
}

func TestSupportedCellRequiresPairedTrustedReleaseInputs(t *testing.T) {
	fixture := newSupportFixture(t)
	fixture.supportCell(t, 16, nil)
	if _, err := VerifyDirectory(fixture.root, fixture.release, VerifyOptions{}); err == nil || !strings.Contains(err.Error(), "requires target and upgrade-from trusted release subjects") {
		t.Fatalf("supported cell without trusted inputs was accepted: %v", err)
	}
	if _, err := VerifyDirectory(fixture.root, fixture.release, VerifyOptions{SourceCommit: fixture.sourceCommit}); err == nil || !strings.Contains(err.Error(), "must be provided together") {
		t.Fatalf("unpaired trusted input was accepted: %v", err)
	}
	if _, err := VerifyDirectory(fixture.root, fixture.release, VerifyOptions{
		ReleaseAssetsDir: fixture.assetsDir,
		SourceCommit:     fixture.sourceCommit,
	}); err == nil || !strings.Contains(err.Error(), "requires target and upgrade-from trusted release subjects") {
		t.Fatalf("supported cell without predecessor inputs was accepted: %v", err)
	}
	if _, err := VerifyDirectory(fixture.root, fixture.release, VerifyOptions{
		UpgradeFromAssetsDir:    fixture.upgradeFromAssetsDir,
		UpgradeFromSourceCommit: fixture.upgradeFromSourceCommit,
	}); err == nil || !strings.Contains(err.Error(), "requires the target trusted release subject") {
		t.Fatalf("predecessor inputs without target inputs were accepted: %v", err)
	}
}

func TestCandidateAndBaselineMayValidateTargetAssetsOnly(t *testing.T) {
	for _, release := range []string{"v1.2.0", "v1.2.1"} {
		t.Run(release, func(t *testing.T) {
			fixture := newSupportFixtureForRelease(t, release)
			_, err := VerifyDirectory(fixture.root, fixture.release, VerifyOptions{
				ReleaseAssetsDir: fixture.assetsDir,
				SourceCommit:     fixture.sourceCommit,
			})
			if err != nil {
				t.Fatalf("candidate target-only asset verification failed: %v", err)
			}
		})
	}
}

func TestSupportedCellRejectsWrongTrustedSourceCommit(t *testing.T) {
	fixture := newSupportFixture(t)
	fixture.supportCell(t, 16, nil)
	_, err := VerifyDirectory(fixture.root, fixture.release, VerifyOptions{
		ReleaseAssetsDir: fixture.assetsDir,
		SourceCommit:     strings.Repeat("e", 40),
	})
	if err == nil || !strings.Contains(err.Error(), "source_commit must be") {
		t.Fatalf("wrong trusted source commit was accepted: %v", err)
	}
}

func TestEvidencePathRejectsIntermediateSymlink(t *testing.T) {
	fixture := newSupportFixture(t)
	fixture.supportCell(t, 16, nil)
	evidenceDirectory := filepath.Join(fixture.root, "evidence")
	externalDirectory := filepath.Join(t.TempDir(), "evidence")
	if err := os.Rename(evidenceDirectory, externalDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalDirectory, evidenceDirectory); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	if _, err := fixture.verify(false); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("intermediate evidence symlink was accepted: %v", err)
	}
}

func TestTrustedReleaseAssetRequiresUniqueBundleManifest(t *testing.T) {
	fixture := newSupportFixture(t)
	fixture.supportCell(t, 16, nil)
	assetName := "probe-panel-management-" + fixture.release + "-linux-amd64.tar.gz"
	manifestName := strings.TrimSuffix(assetName, ".tar.gz") + "/BUNDLE-SHA256SUMS"
	writeTestReleaseAssetEntries(t, filepath.Join(fixture.assetsDir, assetName), fixture.release, "amd64", fixture.sourceCommit, []string{manifestName, manifestName}, nil)
	if _, err := fixture.verify(false); err == nil || !strings.Contains(err.Error(), "exactly one BUNDLE-SHA256SUMS") {
		t.Fatalf("release asset with duplicate bundle manifests was accepted: %v", err)
	}
}

func TestTrustedReleaseAssetRequiresUniqueReleaseManifest(t *testing.T) {
	fixture := newSupportFixture(t)
	assetName := "probe-panel-management-" + fixture.release + "-linux-amd64.tar.gz"
	bundleManifestName := strings.TrimSuffix(assetName, ".tar.gz") + "/BUNDLE-SHA256SUMS"
	writeTestReleaseAssetEntries(t, filepath.Join(fixture.assetsDir, assetName), fixture.release, "amd64", fixture.sourceCommit, []string{bundleManifestName}, nil, 2)
	_, err := VerifyDirectory(fixture.root, fixture.release, VerifyOptions{
		ReleaseAssetsDir: fixture.assetsDir,
		SourceCommit:     fixture.sourceCommit,
	})
	if err == nil || !strings.Contains(err.Error(), "exactly one RELEASE-MANIFEST") {
		t.Fatalf("release asset with duplicate release manifests was accepted: %v", err)
	}
}

func TestTrustedReleaseManifestBindsExactTargetSubject(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
	}{
		{name: "format", old: "format=probe-panel-release-v1", new: "format=weakened"},
		{name: "version", old: "version=v1.2.1", new: "version=v1.2.0"},
		{name: "architecture", old: "architecture=linux-amd64", new: "architecture=linux-arm64"},
		{name: "profile", old: "profile=management", new: "profile=full"},
		{name: "repository", old: "source_repository=Kcmose/super-my", new: "source_repository=someone/else"},
		{name: "source commit", old: "source_commit=" + strings.Repeat("d", 40), new: "source_commit=" + strings.Repeat("e", 40)},
		{name: "source ref", old: "super_my_ref=refs/tags/v1.2.1", new: "super_my_ref=refs/heads/main"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSupportFixture(t)
			assetName := "probe-panel-management-" + fixture.release + "-linux-amd64.tar.gz"
			bundleManifestName := strings.TrimSuffix(assetName, ".tar.gz") + "/BUNDLE-SHA256SUMS"
			releaseManifest := bytes.Replace(testReleaseManifest(fixture.release, "amd64", fixture.sourceCommit), []byte(test.old), []byte(test.new), 1)
			writeTestReleaseAssetEntries(t, filepath.Join(fixture.assetsDir, assetName), fixture.release, "amd64", fixture.sourceCommit, []string{bundleManifestName}, releaseManifest)
			_, err := VerifyDirectory(fixture.root, fixture.release, VerifyOptions{
				ReleaseAssetsDir: fixture.assetsDir,
				SourceCommit:     fixture.sourceCommit,
			})
			if err == nil || !strings.Contains(err.Error(), "RELEASE-MANIFEST") {
				t.Fatalf("tampered target release manifest was accepted: %v", err)
			}
		})
	}
}

func TestTrustedReleaseSubjectRequiresBothArchitectureAssets(t *testing.T) {
	fixture := newSupportFixture(t)
	fixture.supportCell(t, 16, nil)
	arm64Asset := filepath.Join(fixture.assetsDir, "probe-panel-management-"+fixture.release+"-linux-arm64.tar.gz")
	if err := os.Remove(arm64Asset); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.verify(false); err == nil || !strings.Contains(err.Error(), "linux-arm64.tar.gz") {
		t.Fatalf("supported cell was accepted without the other architecture asset: %v", err)
	}
}

func TestDomainEvidenceUsesExclusiveIngressProfile(t *testing.T) {
	fixture := newSupportFixture(t)
	fixture.supportCell(t, 9, func(suite *EvidenceSuite) {
		for index := range suite.Scenarios {
			if suite.Scenarios[index].ID == "coexistence" {
				suite.Scenarios[index].Profile = fixture.policy.ModeProfiles.IP.Coexistence
			}
		}
	})
	if _, err := fixture.verify(false); err == nil || !strings.Contains(err.Error(), "coexistence") {
		t.Fatalf("domain evidence using the IP coexistence strategy was not rejected: %v", err)
	}
}

func TestCentOSRequiresEnforcingAndAdditionalScenario(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*EvidenceSuite)
	}{
		{
			name: "permissive SELinux",
			mutate: func(suite *EvidenceSuite) {
				suite.Environment.SELinuxMode = "Permissive"
			},
		},
		{
			name: "missing Enforcing scenario",
			mutate: func(suite *EvidenceSuite) {
				for index := range suite.Scenarios {
					if suite.Scenarios[index].ID == "selinux_enforcing" {
						suite.Scenarios = append(suite.Scenarios[:index], suite.Scenarios[index+1:]...)
						return
					}
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSupportFixture(t)
			fixture.supportCell(t, 52, test.mutate)
			if _, err := fixture.verify(false); err == nil {
				t.Fatal("incomplete CentOS evidence was accepted")
			}
		})
	}
}

func TestEOLPlatformRequiresRepositoryScenario(t *testing.T) {
	fixture := newSupportFixture(t)
	fixture.supportCell(t, 0, func(suite *EvidenceSuite) {
		for index := range suite.Scenarios {
			if suite.Scenarios[index].ID == "eol_repository" {
				suite.Scenarios = append(suite.Scenarios[:index], suite.Scenarios[index+1:]...)
				return
			}
		}
	})
	if _, err := fixture.verify(false); err == nil || !strings.Contains(err.Error(), "required scenarios") {
		t.Fatalf("EOL evidence without its repository scenario was not rejected: %v", err)
	}
}

func TestCentOSEOLCellPassesWithBothAdditionalGates(t *testing.T) {
	fixture := newSupportFixture(t)
	fixture.supportCell(t, 40, nil)
	summary, err := fixture.verify(false)
	if err != nil {
		t.Fatalf("complete CentOS EOL evidence was rejected: %v", err)
	}
	if summary.Supported != 1 || summary.SupportedPlatforms != 0 {
		t.Fatalf("CentOS EOL cell summary is incorrect: %+v", summary)
	}
}

func TestEvidenceEnvironmentAndArtifactsAreDurable(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*EvidenceSuite)
	}{
		{
			name: "container",
			mutate: func(suite *EvidenceSuite) {
				suite.Environment.Kind = "container"
			},
		},
		{
			name: "same boot ID",
			mutate: func(suite *EvidenceSuite) {
				suite.Environment.BootIDAfter = suite.Environment.BootIDBefore
			},
		},
		{
			name: "transient Actions artifact",
			mutate: func(suite *EvidenceSuite) {
				suite.Artifacts[0].URI = "https://github.com/Kcmose/super-my/actions/runs/1/artifacts/2"
			},
		},
		{
			name: "artifact from another repository",
			mutate: func(suite *EvidenceSuite) {
				suite.Artifacts[0].URI = strings.Replace(suite.Artifacts[0].URI, "Kcmose/super-my", "someone/else", 1)
			},
		},
		{
			name: "unrelated release artifact",
			mutate: func(suite *EvidenceSuite) {
				suite.Artifacts[0].URI = "https://github.com/Kcmose/super-my/releases/download/v1.2.1/unrelated.tar.gz"
			},
		},
		{
			name: "release artifact with dot segment",
			mutate: func(suite *EvidenceSuite) {
				suite.Artifacts[0].URI = "https://github.com/Kcmose/super-my/releases/download/v1.2.1/../unrelated.tar.gz"
			},
		},
		{
			name: "release artifact with empty query",
			mutate: func(suite *EvidenceSuite) {
				suite.Artifacts[0].URI += "?"
			},
		},
		{
			name: "wrong embedded cell",
			mutate: func(suite *EvidenceSuite) {
				suite.Cell.Ingress = "domain"
			},
		},
		{
			name: "wrong release",
			mutate: func(suite *EvidenceSuite) {
				suite.Release = "v1.2.2"
			},
		},
		{
			name: "wrong upgrade predecessor",
			mutate: func(suite *EvidenceSuite) {
				suite.Subject.UpgradeFromRelease = "v1.1.0"
			},
		},
		{
			name: "wrong bundle hash",
			mutate: func(suite *EvidenceSuite) {
				suite.Subject.AssetSHA256 = strings.Repeat("e", 64)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSupportFixture(t)
			fixture.supportCell(t, 8, test.mutate)
			if _, err := fixture.verify(false); err == nil {
				t.Fatal("invalid evidence was accepted")
			}
		})
	}
}

func TestPlatformRollupRequiresAllFourCells(t *testing.T) {
	fixture := newSupportFixture(t)
	for _, index := range []int{8, 9, 10, 11} {
		fixture.supportCell(t, index, nil)
	}
	summary, err := fixture.verify(false)
	if err != nil {
		t.Fatalf("complete platform evidence was rejected: %v", err)
	}
	if summary.Supported != 4 || summary.SupportedPlatforms != 1 {
		t.Fatalf("complete platform was not rolled up: %+v", summary)
	}
	for _, platform := range summary.Platforms {
		if platform.PlatformID == "debian-11-systemd" && !platform.FormalSupported {
			t.Fatal("all four Debian 11 cells did not produce formal platform support")
		}
	}
}

func newSupportFixture(t *testing.T) *supportFixture {
	t.Helper()
	return newSupportFixtureForRelease(t, "v1.2.1")
}

func newSupportFixtureForRelease(t *testing.T, release string) *supportFixture {
	t.Helper()
	root := t.TempDir()
	policy := canonicalPolicy()
	upgradeFromRelease, promotionEligible := promotionPredecessor(policy, release)
	sourceCommit := strings.Repeat("d", 40)
	upgradeFromSourceCommit := strings.Repeat("c", 40)
	if err := os.MkdirAll(filepath.Join(root, "releases"), 0o700); err != nil {
		t.Fatal(err)
	}
	assetsDir := filepath.Join(root, "release-assets")
	if err := os.MkdirAll(assetsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, architecture := range canonicalArchitectures {
		writeTestReleaseAsset(t, assetsDir, release, architecture, sourceCommit)
	}
	assets, err := loadTrustedReleaseAssets(assetsDir, release, sourceCommit)
	if err != nil {
		t.Fatal(err)
	}
	upgradeFromAssetsDir := ""
	upgradeFromAssets := make(map[string]trustedAsset)
	if promotionEligible {
		upgradeFromAssetsDir = filepath.Join(root, "upgrade-from-release-assets")
		if err := os.MkdirAll(upgradeFromAssetsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, architecture := range canonicalArchitectures {
			writeTestReleaseAsset(t, upgradeFromAssetsDir, upgradeFromRelease, architecture, upgradeFromSourceCommit)
		}
		upgradeFromAssets, err = loadTrustedReleaseAssets(upgradeFromAssetsDir, upgradeFromRelease, upgradeFromSourceCommit)
		if err != nil {
			t.Fatal(err)
		}
	}
	fixture := &supportFixture{
		root:                    root,
		release:                 release,
		policy:                  policy,
		assetsDir:               assetsDir,
		sourceCommit:            sourceCommit,
		assets:                  assets,
		upgradeFromRelease:      upgradeFromRelease,
		upgradeFromAssetsDir:    upgradeFromAssetsDir,
		upgradeFromSourceCommit: upgradeFromSourceCommit,
		upgradeFromAssets:       upgradeFromAssets,
		ledger: ReleaseLedger{
			Schema:            releaseSchema,
			Release:           release,
			RuntimeABI:        runtimeABI,
			PromotionEligible: &promotionEligible,
			Cells:             canonicalCells(),
		},
	}
	writeJSON(t, filepath.Join(root, "policy-v1.json"), fixture.policy)
	fixture.writeLedger(t)
	return fixture
}

func (fixture *supportFixture) verify(requireZeroSupported bool) (Summary, error) {
	options := VerifyOptions{
		RequireZeroSupported: requireZeroSupported,
		ReleaseAssetsDir:     fixture.assetsDir,
		SourceCommit:         fixture.sourceCommit,
	}
	if fixture.upgradeFromRelease != "" {
		options.UpgradeFromAssetsDir = fixture.upgradeFromAssetsDir
		options.UpgradeFromSourceCommit = fixture.upgradeFromSourceCommit
	}
	return VerifyDirectory(fixture.root, fixture.release, options)
}

func (fixture *supportFixture) writeLedger(t *testing.T) {
	t.Helper()
	writeJSON(t, filepath.Join(fixture.root, "releases", fixture.release+".json"), fixture.ledger)
}

func (fixture *supportFixture) supportCell(t *testing.T, index int, mutate func(*EvidenceSuite)) {
	t.Helper()
	claim := &fixture.ledger.Cells[index]
	platform, ok := findPlatform(fixture.policy.Platforms, claim.PlatformID)
	if !ok {
		t.Fatalf("test cell has unknown platform: %s", claim.PlatformID)
	}
	suite := validEvidenceSuite(fixture.policy, fixture.release, platform, *claim, fixture.sourceCommit, fixture.assets, fixture.upgradeFromSourceCommit, fixture.upgradeFromAssets)
	claim.SourceCommit = suite.Subject.SourceCommit
	claim.AssetSHA256 = suite.Subject.AssetSHA256
	claim.BundleManifestSHA256 = suite.Subject.BundleManifestSHA256
	if mutate != nil {
		mutate(&suite)
	}
	data, err := json.MarshalIndent(suite, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	relative := filepath.ToSlash(filepath.Join("evidence", fixture.release, strings.ReplaceAll(cellKey(claim.PlatformID, claim.Architecture, claim.Ingress), "/", "-")+".json"))
	filename := filepath.Join(fixture.root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	claim.Claim = claimSupported
	claim.Evidence = relative
	claim.EvidenceSHA256 = hex.EncodeToString(digest[:])
	fixture.writeLedger(t)
}

func validEvidenceSuite(policy Policy, release string, platform PlatformPolicy, claim CellClaim, sourceCommit string, assets map[string]trustedAsset, upgradeFromSourceCommit string, upgradeFromAssets map[string]trustedAsset) EvidenceSuite {
	hashB := strings.Repeat("b", 64)
	hashC := strings.Repeat("c", 64)
	asset := assets[claim.Architecture]
	upgradeFromAsset := upgradeFromAssets[claim.Architecture]
	upgradeFromRelease, _ := promotionPredecessor(policy, release)
	machine := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[claim.Architecture]
	ids := append([]string(nil), policy.BaseScenarios...)
	if platform.RequiresSELinuxEnforcing {
		ids = append(ids, policy.CentOSAdditionalScenarios...)
	}
	if platform.EOL {
		ids = append(ids, policy.EOLAdditionalScenarios...)
	}
	scenarios := make([]ScenarioEvidence, 0, len(ids))
	for _, id := range ids {
		scenarios = append(scenarios, ScenarioEvidence{
			ID:      id,
			Profile: scenarioProfile(policy, claim.Ingress, id),
			Result:  "pass",
		})
	}
	return EvidenceSuite{
		Schema:     evidenceSchema,
		Release:    release,
		RuntimeABI: runtimeABI,
		Cell: EvidenceCell{
			PlatformID:   claim.PlatformID,
			Architecture: claim.Architecture,
			Ingress:      claim.Ingress,
		},
		Subject: EvidenceSubject{
			SourceTag:                       "refs/tags/" + release,
			SourceCommit:                    sourceCommit,
			UpgradeFromRelease:              upgradeFromRelease,
			UpgradeFromSourceCommit:         map[bool]string{true: upgradeFromSourceCommit}[upgradeFromRelease != ""],
			UpgradeFromAssetSHA256:          upgradeFromAsset.SHA256,
			UpgradeFromBundleManifestSHA256: upgradeFromAsset.BundleManifestSHA256,
			Asset:                           asset.Name,
			AssetSHA256:                     asset.SHA256,
			BundleManifestSHA256:            asset.BundleManifestSHA256,
		},
		Environment: EvidenceEnvironment{
			Kind:            fullSystemVM,
			ImageID:         "fixture-" + claim.PlatformID,
			ImageSHA256:     hashB,
			OSReleaseSHA256: hashC,
			Machine:         machine,
			PID1Systemd:     true,
			SELinuxMode:     map[bool]string{true: "Enforcing", false: ""}[platform.RequiresSELinuxEnforcing],
			BootIDBefore:    "11111111-1111-4111-8111-111111111111",
			BootIDAfter:     "22222222-2222-4222-8222-222222222222",
		},
		Scenarios: scenarios,
		Artifacts: []ArtifactEvidence{
			{
				URI:       "https://github.com/" + policy.SourceRepository + "/releases/download/" + release + "/" + claim.PlatformID + "-" + claim.Architecture + "-" + claim.Ingress + ".tar.gz",
				SHA256:    hashC,
				SizeBytes: 1024,
			},
		},
		Reviewer: "release-engineering@example.invalid",
	}
}

func writeTestReleaseAsset(t *testing.T, assetsDir, release, architecture, sourceCommit string) {
	t.Helper()
	assetName := "probe-panel-management-" + release + "-linux-" + architecture + ".tar.gz"
	manifestName := strings.TrimSuffix(assetName, ".tar.gz") + "/BUNDLE-SHA256SUMS"
	writeTestReleaseAssetEntries(t, filepath.Join(assetsDir, assetName), release, architecture, sourceCommit, []string{manifestName}, nil)
}

func writeTestReleaseAssetEntries(t *testing.T, filename, release, architecture, sourceCommit string, manifestNames []string, releaseManifest []byte, releaseManifestCopies ...int) {
	t.Helper()
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	manifest := []byte("fixture  artifacts/api/probe-api\n")
	for _, manifestName := range manifestNames {
		if err := tarWriter.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o644, Size: int64(len(manifest)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(manifest); err != nil {
			t.Fatal(err)
		}
	}
	assetName := filepath.Base(filename)
	releaseManifestName := strings.TrimSuffix(assetName, ".tar.gz") + "/RELEASE-MANIFEST"
	if releaseManifest == nil {
		releaseManifest = testReleaseManifest(release, architecture, sourceCommit)
	}
	copies := 1
	if len(releaseManifestCopies) == 1 {
		copies = releaseManifestCopies[0]
	}
	for index := 0; index < copies; index++ {
		if err := tarWriter.WriteHeader(&tar.Header{Name: releaseManifestName, Mode: 0o644, Size: int64(len(releaseManifest)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(releaseManifest); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func testReleaseManifest(release, architecture, sourceCommit string) []byte {
	platformIDs := make([]string, 0, len(canonicalPolicy().Platforms))
	for _, platform := range canonicalPolicy().Platforms {
		platformIDs = append(platformIDs, platform.ID)
	}
	return []byte(fmt.Sprintf("format=probe-panel-release-v1\nversion=%s\narchitecture=linux-%s\nprofile=management\nruntime_abi=%s\nplatform_ids=%s\nsource_repository=%s\nsource_commit=%s\nsuper_my_ref=refs/tags/%s\n", release, architecture, runtimeABI, strings.Join(platformIDs, ","), sourceRepository, sourceCommit, release))
}

func writeJSON(t *testing.T, filename string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
