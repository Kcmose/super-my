// Package supportevidence validates the formal-support evidence ledger.
//
// Compatibility and formal support are deliberately separate. A platform can
// be accepted by the installer while every release matrix cell remains a
// candidate until a complete, immutable evidence suite is reviewed and the
// corresponding ledger claim is explicitly changed to supported.
package supportevidence

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
)

const (
	policySchema                    = "probe-support-policy-v1"
	releaseSchema                   = "probe-support-release-v1"
	evidenceSchema                  = "probe-support-evidence-v1"
	runtimeABI                      = "probe-linux-systemd-v2"
	claimCandidate                  = "candidate"
	claimSupported                  = "supported"
	fullSystemVM                    = "full-system-vm"
	sourceRepository                = "Kcmose/super-my"
	firstPromotableRelease          = "v1.2.1"
	maxJSONSize               int64 = 1 << 20
	maxManifestSize           int64 = 16 << 20
	maxBundleUncompressedSize int64 = 2 << 30
)

var (
	releasePattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	sha256Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	bootIDPattern  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

	canonicalArchitectures = []string{"amd64", "arm64"}
	canonicalIngressModes  = []string{"ip", "domain"}
	canonicalBaseScenarios = []string{
		"fresh",
		"coexistence",
		"conflict",
		"no_mutation",
		"reboot",
		"upgrade",
		"fault",
		"backup_restore",
		"uninstall",
	}
	canonicalCentOSScenarios = []string{"selinux_enforcing"}
	canonicalEOLScenarios    = []string{"eol_repository"}
)

// Policy is the pinned, non-weakenable definition of the formal-support
// matrix and required evidence scenarios.
type Policy struct {
	Schema                    string             `json:"schema"`
	RuntimeABI                string             `json:"runtime_abi"`
	SourceRepository          string             `json:"source_repository"`
	FirstPromotableRelease    string             `json:"first_promotable_release"`
	PromotionLineage          []PromotionLineage `json:"promotion_lineage"`
	Architectures             []string           `json:"architectures"`
	IngressModes              []string           `json:"ingress_modes"`
	BaseScenarios             []string           `json:"base_scenarios"`
	ModeProfiles              ModeProfiles       `json:"mode_profiles"`
	CentOSAdditionalScenarios []string           `json:"centos_additional_scenarios"`
	EOLAdditionalScenarios    []string           `json:"eol_additional_scenarios"`
	Platforms                 []PlatformPolicy   `json:"platforms"`
}

// PromotionLineage pins the exact predecessor whose released management
// bundle must be exercised by the upgrade scenario. Releases absent from this
// list are not promotion eligible.
type PromotionLineage struct {
	Release            string `json:"release"`
	UpgradeFromRelease string `json:"upgrade_from_release"`
}

// ModeProfiles distinguishes the supported coexistence and conflict behavior
// for IP and domain installations.
type ModeProfiles struct {
	IP     ScenarioProfiles `json:"ip"`
	Domain ScenarioProfiles `json:"domain"`
}

// ScenarioProfiles binds the scenarios whose meaning differs by ingress mode.
type ScenarioProfiles struct {
	Coexistence string `json:"coexistence"`
	Conflict    string `json:"conflict"`
	NoMutation  string `json:"no_mutation"`
}

// PlatformPolicy records immutable metadata for one exact OS release.
type PlatformPolicy struct {
	ID                       string `json:"id"`
	Family                   string `json:"family"`
	EOL                      bool   `json:"eol"`
	RequiresSELinuxEnforcing bool   `json:"requires_selinux_enforcing"`
}

// ReleaseLedger contains the explicit 15 x 2 x 2 release matrix.
type ReleaseLedger struct {
	Schema            string      `json:"schema"`
	Release           string      `json:"release"`
	RuntimeABI        string      `json:"runtime_abi"`
	PromotionEligible *bool       `json:"promotion_eligible"`
	Cells             []CellClaim `json:"cells"`
}

// CellClaim is the reviewed human claim for one exact matrix cell. Complete
// evidence does not promote a candidate automatically.
type CellClaim struct {
	PlatformID           string `json:"platform_id"`
	Architecture         string `json:"architecture"`
	Ingress              string `json:"ingress"`
	Claim                string `json:"claim"`
	Evidence             string `json:"evidence,omitempty"`
	EvidenceSHA256       string `json:"evidence_sha256,omitempty"`
	SourceCommit         string `json:"source_commit,omitempty"`
	AssetSHA256          string `json:"asset_sha256,omitempty"`
	BundleManifestSHA256 string `json:"bundle_manifest_sha256,omitempty"`
}

// EvidenceSuite is a complete formal-support test run bound to one cell and
// one immutable release subject.
type EvidenceSuite struct {
	Schema      string              `json:"schema"`
	Release     string              `json:"release"`
	RuntimeABI  string              `json:"runtime_abi"`
	Cell        EvidenceCell        `json:"cell"`
	Subject     EvidenceSubject     `json:"subject"`
	Environment EvidenceEnvironment `json:"environment"`
	Scenarios   []ScenarioEvidence  `json:"scenarios"`
	Artifacts   []ArtifactEvidence  `json:"artifacts"`
	Reviewer    string              `json:"reviewer"`
}

// EvidenceCell is the cell identity embedded in an evidence suite.
type EvidenceCell struct {
	PlatformID   string `json:"platform_id"`
	Architecture string `json:"architecture"`
	Ingress      string `json:"ingress"`
}

// EvidenceSubject binds results to the immutable source and release bundle.
type EvidenceSubject struct {
	SourceTag                       string `json:"source_tag"`
	SourceCommit                    string `json:"source_commit"`
	UpgradeFromRelease              string `json:"upgrade_from_release"`
	UpgradeFromSourceCommit         string `json:"upgrade_from_source_commit"`
	UpgradeFromAssetSHA256          string `json:"upgrade_from_asset_sha256"`
	UpgradeFromBundleManifestSHA256 string `json:"upgrade_from_bundle_manifest_sha256"`
	Asset                           string `json:"asset"`
	AssetSHA256                     string `json:"asset_sha256"`
	BundleManifestSHA256            string `json:"bundle_manifest_sha256"`
}

// VerifyOptions supplies the trust root that cannot be taken from the
// editable evidence ledger itself. ReleaseAssetsDir and SourceCommit must be
// supplied together. Candidate-only ledgers may omit both.
type VerifyOptions struct {
	RequireZeroSupported    bool
	ReleaseAssetsDir        string
	SourceCommit            string
	UpgradeFromAssetsDir    string
	UpgradeFromSourceCommit string
}

type trustedAsset struct {
	Name                 string
	SHA256               string
	BundleManifestSHA256 string
}

// EvidenceEnvironment proves the scenarios ran on a full system with systemd
// as PID 1 and includes the two boot identities needed to prove a real reboot.
type EvidenceEnvironment struct {
	Kind            string `json:"kind"`
	ImageID         string `json:"image_id"`
	ImageSHA256     string `json:"image_sha256"`
	OSReleaseSHA256 string `json:"os_release_sha256"`
	Machine         string `json:"machine"`
	PID1Systemd     bool   `json:"pid1_systemd"`
	SELinuxMode     string `json:"selinux_mode,omitempty"`
	BootIDBefore    string `json:"boot_id_before"`
	BootIDAfter     string `json:"boot_id_after"`
}

// ScenarioEvidence is one required scenario result.
type ScenarioEvidence struct {
	ID      string `json:"id"`
	Profile string `json:"profile"`
	Result  string `json:"result"`
}

// ArtifactEvidence references durable output whose content is pinned by hash.
type ArtifactEvidence struct {
	URI       string `json:"uri"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

// Summary is the machine-readable result emitted by the gate.
type Summary struct {
	Schema             string            `json:"schema"`
	Release            string            `json:"release"`
	RuntimeABI         string            `json:"runtime_abi"`
	Cells              int               `json:"cells"`
	Candidate          int               `json:"candidate"`
	Supported          int               `json:"supported"`
	SupportedPlatforms int               `json:"supported_platforms"`
	Platforms          []PlatformSummary `json:"platforms"`
}

// PlatformSummary prevents a partial architecture or ingress result from
// being presented as broad platform support.
type PlatformSummary struct {
	PlatformID      string `json:"platform_id"`
	Candidate       int    `json:"candidate"`
	Supported       int    `json:"supported"`
	FormalSupported bool   `json:"formal_supported"`
}

// VerifyDirectory validates policy-v1.json, releases/<release>.json and every
// evidence suite referenced by that release ledger.
func VerifyDirectory(supportRoot, release string, options VerifyOptions) (Summary, error) {
	var summary Summary
	if !releasePattern.MatchString(release) {
		return summary, fmt.Errorf("release must be an exact vMAJOR.MINOR.PATCH value: %q", release)
	}
	root, err := canonicalDirectory(supportRoot)
	if err != nil {
		return summary, err
	}

	targetSubjectProvided := options.ReleaseAssetsDir != "" || options.SourceCommit != ""
	upgradeSubjectProvided := options.UpgradeFromAssetsDir != "" || options.UpgradeFromSourceCommit != ""
	if (options.ReleaseAssetsDir == "") != (options.SourceCommit == "") {
		return summary, errors.New("release_assets_dir and source_commit must be provided together")
	}
	if (options.UpgradeFromAssetsDir == "") != (options.UpgradeFromSourceCommit == "") {
		return summary, errors.New("upgrade_from_assets_dir and upgrade_from_source_commit must be provided together")
	}
	if upgradeSubjectProvided && !targetSubjectProvided {
		return summary, errors.New("upgrade-from trusted release subject requires the target trusted release subject")
	}
	if options.SourceCommit != "" && !commitPattern.MatchString(options.SourceCommit) {
		return summary, errors.New("source_commit must be a lowercase 40-character Git object ID")
	}
	if options.UpgradeFromSourceCommit != "" && !commitPattern.MatchString(options.UpgradeFromSourceCommit) {
		return summary, errors.New("upgrade_from_source_commit must be a lowercase 40-character Git object ID")
	}

	var policy Policy
	if err := readStrictJSONWithin(root, "policy-v1.json", &policy); err != nil {
		return summary, fmt.Errorf("policy: %w", err)
	}
	if err := validatePolicy(policy); err != nil {
		return summary, fmt.Errorf("policy: %w", err)
	}

	var ledger ReleaseLedger
	ledgerRelative := path.Join("releases", release+".json")
	if err := readStrictJSONWithin(root, ledgerRelative, &ledger); err != nil {
		return summary, fmt.Errorf("release ledger: %w", err)
	}
	if err := validateLedgerHeader(ledger, release, policy); err != nil {
		return summary, fmt.Errorf("release ledger: %w", err)
	}

	trustedAssets := make(map[string]trustedAsset)
	trustedUpgradeAssets := make(map[string]trustedAsset)
	if options.ReleaseAssetsDir != "" {
		trustedAssets, err = loadTrustedReleaseAssets(options.ReleaseAssetsDir, release, options.SourceCommit)
		if err != nil {
			return summary, fmt.Errorf("release assets: %w", err)
		}
	}
	if options.UpgradeFromAssetsDir != "" {
		upgradeFromRelease, promotable := promotionPredecessor(policy, release)
		if !promotable {
			return summary, fmt.Errorf("release %s has no promotion lineage for trusted upgrade inputs", release)
		}
		trustedUpgradeAssets, err = loadTrustedReleaseAssets(options.UpgradeFromAssetsDir, upgradeFromRelease, options.UpgradeFromSourceCommit)
		if err != nil {
			return summary, fmt.Errorf("upgrade-from release assets: %w", err)
		}
	}

	expectedCells := canonicalCells()
	if len(ledger.Cells) != len(expectedCells) {
		return summary, fmt.Errorf("release ledger: expected exactly %d cells, found %d", len(expectedCells), len(ledger.Cells))
	}
	seenCells := make(map[string]struct{}, len(ledger.Cells))
	seenEvidence := make(map[string]struct{})
	architectureSubjects := make(map[string]string)
	evidenceSourceCommit := ""
	platformCounts := make(map[string]*PlatformSummary, len(policy.Platforms))
	for _, platform := range policy.Platforms {
		platformCounts[platform.ID] = &PlatformSummary{PlatformID: platform.ID}
	}

	for index, claim := range ledger.Cells {
		key := cellKey(claim.PlatformID, claim.Architecture, claim.Ingress)
		if _, exists := seenCells[key]; exists {
			return summary, fmt.Errorf("release ledger: duplicate cell %s", key)
		}
		seenCells[key] = struct{}{}
		expected := expectedCells[index]
		if claim.PlatformID != expected.PlatformID || claim.Architecture != expected.Architecture || claim.Ingress != expected.Ingress {
			return summary, fmt.Errorf("release ledger: cell %d is not in canonical order; expected %s, found %s", index, cellKey(expected.PlatformID, expected.Architecture, expected.Ingress), key)
		}
		if claim.Claim != claimCandidate && claim.Claim != claimSupported {
			return summary, fmt.Errorf("release ledger: cell %s has invalid claim %q", key, claim.Claim)
		}
		if claim.Claim == claimSupported && !*ledger.PromotionEligible {
			return summary, fmt.Errorf("release ledger: release %s is a candidate baseline and is not promotion eligible", release)
		}
		if claim.Claim == claimSupported && (!targetSubjectProvided || !upgradeSubjectProvided) {
			return summary, fmt.Errorf("release ledger: supported cell %s requires target and upgrade-from trusted release subjects", key)
		}
		if (claim.Evidence == "") != (claim.EvidenceSHA256 == "") {
			return summary, fmt.Errorf("release ledger: cell %s must provide both evidence and evidence_sha256", key)
		}
		hasSubject := claim.SourceCommit != "" || claim.AssetSHA256 != "" || claim.BundleManifestSHA256 != ""
		if claim.Evidence == "" && hasSubject {
			return summary, fmt.Errorf("release ledger: cell %s has release-subject hashes without evidence", key)
		}
		if claim.Evidence != "" && !hasSubject {
			return summary, fmt.Errorf("release ledger: cell %s has evidence without release-subject hashes", key)
		}
		if claim.Claim == claimSupported && claim.Evidence == "" {
			return summary, fmt.Errorf("release ledger: supported cell %s has no evidence suite", key)
		}
		if claim.Evidence != "" {
			if _, exists := seenEvidence[claim.Evidence]; exists {
				return summary, fmt.Errorf("release ledger: evidence suite is reused: %s", claim.Evidence)
			}
			seenEvidence[claim.Evidence] = struct{}{}
			platform, ok := findPlatform(policy.Platforms, claim.PlatformID)
			if !ok {
				return summary, fmt.Errorf("release ledger: unknown platform %q", claim.PlatformID)
			}
			if err := validateClaimEvidence(root, release, policy, platform, claim, options.SourceCommit, trustedAssets, options.UpgradeFromSourceCommit, trustedUpgradeAssets); err != nil {
				return summary, fmt.Errorf("release ledger: cell %s: %w", key, err)
			}
			if evidenceSourceCommit == "" {
				evidenceSourceCommit = claim.SourceCommit
			} else if evidenceSourceCommit != claim.SourceCommit {
				return summary, fmt.Errorf("release ledger: cell %s does not use the same source commit as other evidence", key)
			}
			subject := claim.SourceCommit + "/" + claim.AssetSHA256 + "/" + claim.BundleManifestSHA256
			if previous, exists := architectureSubjects[claim.Architecture]; exists && previous != subject {
				return summary, fmt.Errorf("release ledger: cell %s does not use the same immutable %s bundle as other evidence", key, claim.Architecture)
			}
			architectureSubjects[claim.Architecture] = subject
		}
		counts := platformCounts[claim.PlatformID]
		if claim.Claim == claimSupported {
			summary.Supported++
			counts.Supported++
		} else {
			summary.Candidate++
			counts.Candidate++
		}
	}

	if options.RequireZeroSupported && summary.Supported != 0 {
		return Summary{}, fmt.Errorf("release ledger: expected zero supported cells, found %d", summary.Supported)
	}
	summary.Schema = "probe-support-summary-v1"
	summary.Release = release
	summary.RuntimeABI = runtimeABI
	summary.Cells = len(ledger.Cells)
	for _, platform := range policy.Platforms {
		counts := *platformCounts[platform.ID]
		counts.FormalSupported = counts.Supported == len(canonicalArchitectures)*len(canonicalIngressModes)
		if counts.FormalSupported {
			summary.SupportedPlatforms++
		}
		summary.Platforms = append(summary.Platforms, counts)
	}
	return summary, nil
}

func canonicalPolicy() Policy {
	return Policy{
		Schema:                 policySchema,
		RuntimeABI:             runtimeABI,
		SourceRepository:       sourceRepository,
		FirstPromotableRelease: firstPromotableRelease,
		PromotionLineage: []PromotionLineage{
			{Release: "v1.2.1", UpgradeFromRelease: "v1.2.0"},
		},
		Architectures: append([]string(nil), canonicalArchitectures...),
		IngressModes:  append([]string(nil), canonicalIngressModes...),
		BaseScenarios: append([]string(nil), canonicalBaseScenarios...),
		ModeProfiles: ModeProfiles{
			IP: ScenarioProfiles{
				Coexistence: "ip-active-native-nginx-loopback-postgres-v1",
				Conflict:    "ip-owned-listener-conflict-v1",
				NoMutation:  "ip-conflict-no-mutation-v1",
			},
			Domain: ScenarioProfiles{
				Coexistence: "domain-exclusive-probe-nginx-loopback-postgres-v1",
				Conflict:    "domain-active-native-nginx-80-443-conflict-v1",
				NoMutation:  "domain-active-native-nginx-no-mutation-v1",
			},
		},
		CentOSAdditionalScenarios: append([]string(nil), canonicalCentOSScenarios...),
		EOLAdditionalScenarios:    append([]string(nil), canonicalEOLScenarios...),
		Platforms: []PlatformPolicy{
			{ID: "debian-9-systemd", Family: "debian", EOL: true},
			{ID: "debian-10-systemd", Family: "debian", EOL: true},
			{ID: "debian-11-systemd", Family: "debian", EOL: true},
			{ID: "debian-12-systemd", Family: "debian", EOL: true},
			{ID: "debian-13-systemd", Family: "debian"},
			{ID: "ubuntu-18.04-systemd", Family: "ubuntu", EOL: true},
			{ID: "ubuntu-20.04-systemd", Family: "ubuntu", EOL: true},
			{ID: "ubuntu-22.04-systemd", Family: "ubuntu"},
			{ID: "ubuntu-24.04-systemd", Family: "ubuntu"},
			{ID: "ubuntu-26.04-systemd", Family: "ubuntu"},
			{ID: "centos-linux-7-systemd", Family: "centos", EOL: true, RequiresSELinuxEnforcing: true},
			{ID: "centos-linux-8-systemd", Family: "centos", EOL: true, RequiresSELinuxEnforcing: true},
			{ID: "centos-stream-8-systemd", Family: "centos", EOL: true, RequiresSELinuxEnforcing: true},
			{ID: "centos-stream-9-systemd", Family: "centos", RequiresSELinuxEnforcing: true},
			{ID: "centos-stream-10-systemd", Family: "centos", RequiresSELinuxEnforcing: true},
		},
	}
}

func canonicalCells() []CellClaim {
	policy := canonicalPolicy()
	cells := make([]CellClaim, 0, len(policy.Platforms)*len(canonicalArchitectures)*len(canonicalIngressModes))
	for _, platform := range policy.Platforms {
		for _, architecture := range canonicalArchitectures {
			for _, ingress := range canonicalIngressModes {
				cells = append(cells, CellClaim{
					PlatformID:   platform.ID,
					Architecture: architecture,
					Ingress:      ingress,
					Claim:        claimCandidate,
				})
			}
		}
	}
	return cells
}

func validatePolicy(policy Policy) error {
	expected := canonicalPolicy()
	if !reflect.DeepEqual(policy, expected) {
		return errors.New("policy does not exactly match the pinned formal-support contract")
	}
	return nil
}

func validateLedgerHeader(ledger ReleaseLedger, release string, policy Policy) error {
	if ledger.Schema != releaseSchema {
		return fmt.Errorf("schema must be %q", releaseSchema)
	}
	if ledger.Release != release {
		return fmt.Errorf("release must be %q", release)
	}
	if ledger.RuntimeABI != runtimeABI {
		return fmt.Errorf("runtime_abi must be %q", runtimeABI)
	}
	if ledger.PromotionEligible == nil {
		return errors.New("promotion_eligible must be explicitly declared")
	}
	_, expected := promotionPredecessor(policy, release)
	if !expected && !isPromotionBaseline(policy, release) {
		return fmt.Errorf("release %s is not present in the pinned promotion lineage", release)
	}
	if *ledger.PromotionEligible != expected {
		return fmt.Errorf("promotion_eligible must be %t for release %s", expected, release)
	}
	return nil
}

func isPromotionBaseline(policy Policy, release string) bool {
	for _, lineage := range policy.PromotionLineage {
		if lineage.UpgradeFromRelease == release {
			return true
		}
	}
	return false
}

func promotionPredecessor(policy Policy, release string) (string, bool) {
	for _, lineage := range policy.PromotionLineage {
		if lineage.Release == release {
			return lineage.UpgradeFromRelease, true
		}
	}
	return "", false
}

func validateClaimEvidence(root, release string, policy Policy, platform PlatformPolicy, claim CellClaim, trustedSourceCommit string, trustedAssets map[string]trustedAsset, trustedUpgradeSourceCommit string, trustedUpgradeAssets map[string]trustedAsset) error {
	if !sha256Pattern.MatchString(claim.EvidenceSHA256) {
		return errors.New("evidence_sha256 must be a lowercase SHA-256 value")
	}
	if !commitPattern.MatchString(claim.SourceCommit) {
		return errors.New("source_commit must be a lowercase 40-character Git object ID")
	}
	if !sha256Pattern.MatchString(claim.AssetSHA256) {
		return errors.New("asset_sha256 must be a lowercase SHA-256 value")
	}
	if !sha256Pattern.MatchString(claim.BundleManifestSHA256) {
		return errors.New("bundle_manifest_sha256 must be a lowercase SHA-256 value")
	}
	relative, err := canonicalEvidencePath(release, claim.Evidence)
	if err != nil {
		return err
	}
	data, err := readRegularFileWithin(root, relative, maxJSONSize)
	if err != nil {
		return fmt.Errorf("evidence suite: %w", err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != claim.EvidenceSHA256 {
		return errors.New("evidence suite SHA-256 does not match evidence_sha256")
	}
	var suite EvidenceSuite
	if err := decodeStrictJSON(data, &suite); err != nil {
		return fmt.Errorf("evidence suite: %w", err)
	}
	if err := validateEvidenceSuite(suite, release, policy, platform, claim, trustedSourceCommit, trustedAssets, trustedUpgradeSourceCommit, trustedUpgradeAssets); err != nil {
		return fmt.Errorf("evidence suite: %w", err)
	}
	return nil
}

func validateEvidenceSuite(suite EvidenceSuite, release string, policy Policy, platform PlatformPolicy, claim CellClaim, trustedSourceCommit string, trustedAssets map[string]trustedAsset, trustedUpgradeSourceCommit string, trustedUpgradeAssets map[string]trustedAsset) error {
	if suite.Schema != evidenceSchema {
		return fmt.Errorf("schema must be %q", evidenceSchema)
	}
	if suite.Release != release {
		return fmt.Errorf("release must be %q", release)
	}
	if suite.RuntimeABI != runtimeABI {
		return fmt.Errorf("runtime_abi must be %q", runtimeABI)
	}
	if suite.Cell.PlatformID != claim.PlatformID || suite.Cell.Architecture != claim.Architecture || suite.Cell.Ingress != claim.Ingress {
		return errors.New("embedded cell identity does not match the release ledger")
	}
	if suite.Subject.SourceTag != "refs/tags/"+release {
		return fmt.Errorf("source_tag must be %q", "refs/tags/"+release)
	}
	if !commitPattern.MatchString(suite.Subject.SourceCommit) {
		return errors.New("source_commit must be a lowercase 40-character Git object ID")
	}
	if suite.Subject.SourceCommit != claim.SourceCommit {
		return errors.New("source_commit does not match the reviewed release ledger")
	}
	upgradeFromRelease, promotable := promotionPredecessor(policy, release)
	if promotable && suite.Subject.UpgradeFromRelease != upgradeFromRelease {
		return fmt.Errorf("upgrade_from_release must be %q", upgradeFromRelease)
	}
	if promotable {
		if !commitPattern.MatchString(suite.Subject.UpgradeFromSourceCommit) {
			return errors.New("upgrade_from_source_commit must be a lowercase 40-character Git object ID")
		}
		if !sha256Pattern.MatchString(suite.Subject.UpgradeFromAssetSHA256) {
			return errors.New("upgrade_from_asset_sha256 must be a lowercase SHA-256 value")
		}
		if !sha256Pattern.MatchString(suite.Subject.UpgradeFromBundleManifestSHA256) {
			return errors.New("upgrade_from_bundle_manifest_sha256 must be a lowercase SHA-256 value")
		}
	}
	if !promotable && (suite.Subject.UpgradeFromRelease != "" || suite.Subject.UpgradeFromSourceCommit != "" || suite.Subject.UpgradeFromAssetSHA256 != "" || suite.Subject.UpgradeFromBundleManifestSHA256 != "") {
		return errors.New("upgrade-from subject must be empty for a release without promotion lineage")
	}
	expectedAsset := fmt.Sprintf("probe-panel-management-%s-linux-%s.tar.gz", release, claim.Architecture)
	if suite.Subject.Asset != expectedAsset {
		return fmt.Errorf("asset must be %q", expectedAsset)
	}
	if !sha256Pattern.MatchString(suite.Subject.AssetSHA256) {
		return errors.New("asset_sha256 must be a lowercase SHA-256 value")
	}
	if suite.Subject.AssetSHA256 != claim.AssetSHA256 {
		return errors.New("asset_sha256 does not match the reviewed release ledger")
	}
	if !sha256Pattern.MatchString(suite.Subject.BundleManifestSHA256) {
		return errors.New("bundle_manifest_sha256 must be a lowercase SHA-256 value")
	}
	if suite.Subject.BundleManifestSHA256 != claim.BundleManifestSHA256 {
		return errors.New("bundle_manifest_sha256 does not match the reviewed release ledger")
	}
	if claim.Claim == claimSupported {
		if trustedSourceCommit == "" || suite.Subject.SourceCommit != trustedSourceCommit {
			return errors.New("source_commit does not match the trusted release subject")
		}
		trusted, ok := trustedAssets[claim.Architecture]
		if !ok {
			return fmt.Errorf("trusted release subject has no %s asset", claim.Architecture)
		}
		if trusted.Name != suite.Subject.Asset {
			return errors.New("asset does not match the trusted release subject")
		}
		if trusted.SHA256 != suite.Subject.AssetSHA256 {
			return errors.New("asset_sha256 does not match the trusted release asset")
		}
		if trusted.BundleManifestSHA256 != suite.Subject.BundleManifestSHA256 {
			return errors.New("bundle_manifest_sha256 does not match the trusted release asset")
		}
		if trustedUpgradeSourceCommit == "" || suite.Subject.UpgradeFromSourceCommit != trustedUpgradeSourceCommit {
			return errors.New("upgrade_from_source_commit does not match the trusted predecessor release subject")
		}
		trustedUpgrade, ok := trustedUpgradeAssets[claim.Architecture]
		if !ok {
			return fmt.Errorf("trusted predecessor release subject has no %s asset", claim.Architecture)
		}
		if trustedUpgrade.SHA256 != suite.Subject.UpgradeFromAssetSHA256 {
			return errors.New("upgrade_from_asset_sha256 does not match the trusted predecessor release asset")
		}
		if trustedUpgrade.BundleManifestSHA256 != suite.Subject.UpgradeFromBundleManifestSHA256 {
			return errors.New("upgrade_from_bundle_manifest_sha256 does not match the trusted predecessor release asset")
		}
	}
	if err := validateEnvironment(suite.Environment, platform, claim.Architecture); err != nil {
		return err
	}
	if err := validateScenarios(suite.Scenarios, policy, platform, claim.Ingress); err != nil {
		return err
	}
	if len(suite.Artifacts) != 1 {
		return errors.New("exactly one immutable evidence artifact is required")
	}
	for index, artifact := range suite.Artifacts {
		if err := validateArtifact(artifact, policy.SourceRepository, release, claim); err != nil {
			return fmt.Errorf("artifact %d: %w", index, err)
		}
	}
	if strings.TrimSpace(suite.Reviewer) == "" {
		return errors.New("reviewer must be non-empty")
	}
	return nil
}

func validateEnvironment(environment EvidenceEnvironment, platform PlatformPolicy, architecture string) error {
	if environment.Kind != fullSystemVM {
		return fmt.Errorf("environment kind must be %q", fullSystemVM)
	}
	if strings.TrimSpace(environment.ImageID) == "" {
		return errors.New("environment image_id must be non-empty")
	}
	if !sha256Pattern.MatchString(environment.ImageSHA256) {
		return errors.New("environment image_sha256 must be a lowercase SHA-256 value")
	}
	if !sha256Pattern.MatchString(environment.OSReleaseSHA256) {
		return errors.New("environment os_release_sha256 must be a lowercase SHA-256 value")
	}
	expectedMachine := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[architecture]
	if environment.Machine != expectedMachine {
		return fmt.Errorf("environment machine must be %q", expectedMachine)
	}
	if !environment.PID1Systemd {
		return errors.New("environment must prove systemd was PID 1")
	}
	if !bootIDPattern.MatchString(environment.BootIDBefore) || !bootIDPattern.MatchString(environment.BootIDAfter) {
		return errors.New("reboot boot IDs must be lowercase UUIDs")
	}
	if environment.BootIDBefore == environment.BootIDAfter {
		return errors.New("reboot must change the boot ID")
	}
	if platform.RequiresSELinuxEnforcing && environment.SELinuxMode != "Enforcing" {
		return errors.New("CentOS evidence must run with SELinux Enforcing")
	}
	return nil
}

func validateScenarios(scenarios []ScenarioEvidence, policy Policy, platform PlatformPolicy, ingress string) error {
	required := append([]string(nil), policy.BaseScenarios...)
	if platform.RequiresSELinuxEnforcing {
		required = append(required, policy.CentOSAdditionalScenarios...)
	}
	if platform.EOL {
		required = append(required, policy.EOLAdditionalScenarios...)
	}
	if len(scenarios) != len(required) {
		return fmt.Errorf("expected exactly %d required scenarios, found %d", len(required), len(scenarios))
	}
	for index, scenario := range scenarios {
		if scenario.ID != required[index] {
			return fmt.Errorf("scenario %d must be %q", index, required[index])
		}
		if scenario.Result != "pass" {
			return fmt.Errorf("scenario %q result must be pass", scenario.ID)
		}
		expectedProfile := scenarioProfile(policy, ingress, scenario.ID)
		if scenario.Profile != expectedProfile {
			return fmt.Errorf("scenario %q profile must be %q", scenario.ID, expectedProfile)
		}
	}
	return nil
}

func scenarioProfile(policy Policy, ingress, scenario string) string {
	profiles := policy.ModeProfiles.IP
	if ingress == "domain" {
		profiles = policy.ModeProfiles.Domain
	}
	switch scenario {
	case "coexistence":
		return profiles.Coexistence
	case "conflict":
		return profiles.Conflict
	case "no_mutation":
		return profiles.NoMutation
	case "selinux_enforcing":
		return "centos-selinux-enforcing-v1"
	case "eol_repository":
		return "eol-trusted-repository-v1"
	default:
		return scenario + "-v1"
	}
}

func validateArtifact(artifact ArtifactEvidence, repository, release string, claim CellClaim) error {
	parsed, err := url.Parse(artifact.URI)
	if err != nil {
		return fmt.Errorf("invalid URI: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return errors.New("URI must be a query-free GitHub HTTPS release URL")
	}
	expectedURI := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s-%s-%s.tar.gz", repository, release, claim.PlatformID, claim.Architecture, claim.Ingress)
	if parsed.RawPath != "" || path.Clean(parsed.Path) != parsed.Path || artifact.URI != expectedURI {
		return fmt.Errorf("URI must be exactly %s", expectedURI)
	}
	if !sha256Pattern.MatchString(artifact.SHA256) {
		return errors.New("sha256 must be a lowercase SHA-256 value")
	}
	if artifact.SizeBytes <= 0 {
		return errors.New("size_bytes must be positive")
	}
	return nil
}

func canonicalEvidencePath(release, candidate string) (string, error) {
	if candidate == "" || strings.Contains(candidate, "\\") || path.IsAbs(candidate) || path.Clean(candidate) != candidate {
		return "", errors.New("evidence must be a canonical relative slash-separated path")
	}
	prefix := "evidence/" + release + "/"
	if !strings.HasPrefix(candidate, prefix) || candidate == prefix || !strings.HasSuffix(candidate, ".json") {
		return "", fmt.Errorf("evidence must be a JSON file below %s", prefix)
	}
	return candidate, nil
}

func canonicalDirectory(candidate string) (string, error) {
	if candidate == "" {
		return "", errors.New("support root must be non-empty")
	}
	info, err := os.Lstat(candidate)
	if err != nil {
		return "", fmt.Errorf("support root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("support root must be a real directory")
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("support root: %w", err)
	}
	absolute = filepath.Clean(absolute)
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("support root: %w", err)
	}
	if filepath.Clean(resolved) != absolute {
		return "", errors.New("support root path must not contain symbolic links")
	}
	return absolute, nil
}

func loadTrustedReleaseAssets(assetsDir, release, sourceCommit string) (map[string]trustedAsset, error) {
	root, err := canonicalDirectory(assetsDir)
	if err != nil {
		return nil, err
	}
	assets := make(map[string]trustedAsset, len(canonicalArchitectures))
	for _, architecture := range canonicalArchitectures {
		name := fmt.Sprintf("probe-panel-management-%s-linux-%s.tar.gz", release, architecture)
		filename, info, err := regularFilePathWithin(root, name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		assetSHA256, manifestSHA256, err := hashReleaseAsset(filename, name, info, release, architecture, sourceCommit)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		assets[architecture] = trustedAsset{
			Name:                 name,
			SHA256:               assetSHA256,
			BundleManifestSHA256: manifestSHA256,
		}
	}
	return assets, nil
}

func canonicalTarEntryName(header *tar.Header) (string, bool) {
	name := header.Name
	// POSIX tar writers conventionally terminate directory names with one
	// slash. Normalize only that type-specific marker; a trailing slash on any
	// other entry remains invalid.
	if header.Typeflag == tar.TypeDir && strings.HasSuffix(name, "/") {
		name = strings.TrimSuffix(name, "/")
	}
	if name == "" || strings.Contains(name, "\\") || path.IsAbs(name) || path.Clean(name) != name {
		return "", false
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." || component == ".." {
			return "", false
		}
	}
	return name, true
}

func hashReleaseAsset(filename, assetName string, expectedInfo os.FileInfo, release, architecture, sourceCommit string) (string, string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return "", "", err
	}
	if !os.SameFile(expectedInfo, openedInfo) || !openedInfo.Mode().IsRegular() {
		return "", "", errors.New("asset changed while it was being opened")
	}
	assetHash := sha256.New()
	assetReader := io.TeeReader(file, assetHash)
	gzipReader, err := gzip.NewReader(assetReader)
	if err != nil {
		return "", "", fmt.Errorf("open gzip stream: %w", err)
	}
	limitedBundle := &io.LimitedReader{R: gzipReader, N: maxBundleUncompressedSize + 1}
	tarReader := tar.NewReader(limitedBundle)
	bundleRoot := strings.TrimSuffix(assetName, ".tar.gz")
	expectedManifest := bundleRoot + "/BUNDLE-SHA256SUMS"
	expectedReleaseManifest := bundleRoot + "/RELEASE-MANIFEST"
	manifestCount := 0
	releaseManifestCount := 0
	seenEntries := make(map[string]struct{})
	var manifest []byte
	var releaseManifest []byte
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", "", fmt.Errorf("read tar stream: %w", err)
		}
		entryName, canonical := canonicalTarEntryName(header)
		if !canonical {
			return "", "", fmt.Errorf("tar contains non-canonical path %q", header.Name)
		}
		if _, duplicate := seenEntries[entryName]; duplicate {
			return "", "", fmt.Errorf("tar contains duplicate canonical path %q", entryName)
		}
		seenEntries[entryName] = struct{}{}
		baseName := path.Base(entryName)
		if baseName != "BUNDLE-SHA256SUMS" && baseName != "RELEASE-MANIFEST" {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return "", "", fmt.Errorf("%s must be a regular file", baseName)
		}
		if header.Size < 0 || header.Size > maxManifestSize {
			return "", "", fmt.Errorf("%s exceeds %d bytes", baseName, maxManifestSize)
		}
		entry, err := io.ReadAll(io.LimitReader(tarReader, maxManifestSize+1))
		if err != nil {
			return "", "", fmt.Errorf("read %s: %w", baseName, err)
		}
		if int64(len(entry)) != header.Size {
			return "", "", fmt.Errorf("%s size does not match its tar header", baseName)
		}
		switch baseName {
		case "BUNDLE-SHA256SUMS":
			manifestCount++
			if entryName != expectedManifest {
				return "", "", fmt.Errorf("bundle manifest must be exactly %s", expectedManifest)
			}
			manifest = entry
		case "RELEASE-MANIFEST":
			releaseManifestCount++
			if entryName != expectedReleaseManifest {
				return "", "", fmt.Errorf("release manifest must be exactly %s", expectedReleaseManifest)
			}
			releaseManifest = entry
		}
	}
	if manifestCount != 1 {
		return "", "", fmt.Errorf("expected exactly one BUNDLE-SHA256SUMS, found %d", manifestCount)
	}
	if releaseManifestCount != 1 {
		return "", "", fmt.Errorf("expected exactly one RELEASE-MANIFEST, found %d", releaseManifestCount)
	}
	if err := validateReleaseManifest(releaseManifest, release, architecture, sourceCommit); err != nil {
		return "", "", fmt.Errorf("RELEASE-MANIFEST: %w", err)
	}
	if _, err := io.Copy(io.Discard, limitedBundle); err != nil {
		return "", "", fmt.Errorf("validate gzip stream: %w", err)
	}
	if limitedBundle.N == 0 {
		return "", "", fmt.Errorf("uncompressed bundle exceeds %d bytes", maxBundleUncompressedSize)
	}
	if err := gzipReader.Close(); err != nil {
		return "", "", fmt.Errorf("close gzip stream: %w", err)
	}
	if _, err := io.Copy(assetHash, file); err != nil {
		return "", "", fmt.Errorf("hash trailing asset bytes: %w", err)
	}
	manifestHash := sha256.Sum256(manifest)
	return hex.EncodeToString(assetHash.Sum(nil)), hex.EncodeToString(manifestHash[:]), nil
}

func validateReleaseManifest(data []byte, release, architecture, sourceCommit string) error {
	if len(data) == 0 || data[len(data)-1] != '\n' || bytes.Contains(data, []byte{'\r'}) {
		return errors.New("must be non-empty LF-terminated text")
	}
	values := make(map[string]string)
	for index, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" || value == "" || strings.TrimSpace(key) != key || strings.TrimSpace(value) != value {
			return fmt.Errorf("line %d is not a canonical key=value field", index+1)
		}
		if _, exists := values[key]; exists {
			return fmt.Errorf("duplicate field %q", key)
		}
		values[key] = value
	}
	platformIDs := make([]string, 0, len(canonicalPolicy().Platforms))
	for _, platform := range canonicalPolicy().Platforms {
		platformIDs = append(platformIDs, platform.ID)
	}
	expected := map[string]string{
		"format":            "probe-panel-release-v1",
		"profile":           "management",
		"version":           release,
		"architecture":      "linux-" + architecture,
		"runtime_abi":       runtimeABI,
		"platform_ids":      strings.Join(platformIDs, ","),
		"source_repository": sourceRepository,
		"source_commit":     sourceCommit,
		"super_my_ref":      "refs/tags/" + release,
	}
	if len(values) != len(expected) {
		return fmt.Errorf("must contain exactly %d fields, found %d", len(expected), len(values))
	}
	for key, expectedValue := range expected {
		if values[key] != expectedValue {
			return fmt.Errorf("%s must be %q", key, expectedValue)
		}
	}
	return nil
}

func readStrictJSONWithin(root, relative string, destination any) error {
	data, err := readRegularFileWithin(root, relative, maxJSONSize)
	if err != nil {
		return err
	}
	return decodeStrictJSON(data, destination)
}

func readRegularFileWithin(root, relative string, maximum int64) ([]byte, error) {
	filename, info, err := regularFilePathWithin(root, relative)
	if err != nil {
		return nil, err
	}
	if info.Size() > maximum {
		return nil, fmt.Errorf("file exceeds %d bytes", maximum)
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() {
		return nil, errors.New("file changed while it was being opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("file exceeds %d bytes", maximum)
	}
	return data, nil
}

func regularFilePathWithin(root, relative string) (string, os.FileInfo, error) {
	if relative == "" || strings.Contains(relative, "\\") || path.IsAbs(relative) || path.Clean(relative) != relative {
		return "", nil, errors.New("path must be canonical and relative")
	}
	components := strings.Split(relative, "/")
	current := root
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			return "", nil, errors.New("path contains an unsafe component")
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return "", nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", nil, fmt.Errorf("path component %q must not be a symbolic link", component)
		}
		if index < len(components)-1 {
			if !info.IsDir() {
				return "", nil, fmt.Errorf("path component %q must be a directory", component)
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return "", nil, errors.New("must be a regular file")
		}
		return current, info, nil
	}
	return "", nil, errors.New("path has no file component")
}

func decodeStrictJSON(data []byte, destination any) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains a trailing value")
		}
		return fmt.Errorf("JSON trailing data: %w", err)
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := walkJSONValue(decoder, "$"); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains a trailing value")
		}
		return err
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder, location string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("JSON object key at %s is not a string", location)
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON key %q at %s", key, location)
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder, location+"."+key); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return fmt.Errorf("JSON object at %s is not closed", location)
		}
	case '[':
		index := 0
		for decoder.More() {
			if err := walkJSONValue(decoder, fmt.Sprintf("%s[%d]", location, index)); err != nil {
				return err
			}
			index++
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return fmt.Errorf("JSON array at %s is not closed", location)
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q at %s", delimiter, location)
	}
	return nil
}

func findPlatform(platforms []PlatformPolicy, id string) (PlatformPolicy, bool) {
	for _, platform := range platforms {
		if platform.ID == id {
			return platform, true
		}
	}
	return PlatformPolicy{}, false
}

func cellKey(platformID, architecture, ingress string) string {
	return platformID + "/" + architecture + "/" + ingress
}
