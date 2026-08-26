package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"probe-api/internal/supportevidence"
)

func main() {
	if len(os.Args) == 1 || os.Args[1] != "verify" {
		fmt.Fprintln(os.Stderr, "usage: probe-support-gate verify [--support-root DIR] [--release VERSION] [--require-zero-supported] [--release-assets DIR --source-commit COMMIT --source-tag-object OBJECT --upgrade-from-assets DIR --upgrade-from-source-commit COMMIT --upgrade-from-tag-object OBJECT]")
		os.Exit(2)
	}
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	supportRoot := flags.String("support-root", "deploy/support", "formal-support policy, ledgers, and evidence root")
	release := flags.String("release", "v1.2.0", "exact release ledger to verify")
	requireZeroSupported := flags.Bool("require-zero-supported", false, "fail unless every matrix cell remains candidate")
	releaseAssets := flags.String("release-assets", "", "directory containing both immutable management release tarballs")
	sourceCommit := flags.String("source-commit", "", "trusted 40-character source commit for the release tag")
	sourceTagObject := flags.String("source-tag-object", "", "trusted 40-character direct Git object ID for the release tag")
	upgradeFromAssets := flags.String("upgrade-from-assets", "", "directory containing both immutable predecessor management release tarballs")
	upgradeFromSourceCommit := flags.String("upgrade-from-source-commit", "", "trusted 40-character source commit for the predecessor release tag")
	upgradeFromTagObject := flags.String("upgrade-from-tag-object", "", "trusted 40-character direct Git object ID for the predecessor release tag")
	if err := flags.Parse(os.Args[2:]); err != nil {
		os.Exit(2)
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "probe-support-gate: positional arguments are not accepted")
		os.Exit(2)
	}
	targetSubjectProvided := *releaseAssets != "" || *sourceCommit != "" || *sourceTagObject != ""
	upgradeSubjectProvided := *upgradeFromAssets != "" || *upgradeFromSourceCommit != "" || *upgradeFromTagObject != ""
	if targetSubjectProvided && (*releaseAssets == "" || *sourceCommit == "" || *sourceTagObject == "") {
		fmt.Fprintln(os.Stderr, "probe-support-gate: --release-assets, --source-commit, and --source-tag-object must be provided together")
		os.Exit(2)
	}
	if upgradeSubjectProvided && (*upgradeFromAssets == "" || *upgradeFromSourceCommit == "" || *upgradeFromTagObject == "") {
		fmt.Fprintln(os.Stderr, "probe-support-gate: --upgrade-from-assets, --upgrade-from-source-commit, and --upgrade-from-tag-object must be provided together")
		os.Exit(2)
	}
	if upgradeSubjectProvided && !targetSubjectProvided {
		fmt.Fprintln(os.Stderr, "probe-support-gate: upgrade-from trusted release subject requires the target trusted release subject")
		os.Exit(2)
	}
	summary, err := supportevidence.VerifyDirectory(*supportRoot, *release, supportevidence.VerifyOptions{
		RequireZeroSupported:    *requireZeroSupported,
		ReleaseAssetsDir:        *releaseAssets,
		SourceCommit:            *sourceCommit,
		SourceTagObject:         *sourceTagObject,
		UpgradeFromAssetsDir:    *upgradeFromAssets,
		UpgradeFromSourceCommit: *upgradeFromSourceCommit,
		UpgradeFromTagObject:    *upgradeFromTagObject,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe-support-gate: %v\n", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(summary); err != nil {
		fmt.Fprintf(os.Stderr, "probe-support-gate: encode summary: %v\n", err)
		os.Exit(1)
	}
}
