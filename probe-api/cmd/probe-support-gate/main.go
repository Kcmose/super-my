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
		fmt.Fprintln(os.Stderr, "usage: probe-support-gate verify [--support-root DIR] [--release VERSION] [--require-zero-supported] [--release-assets DIR --source-commit COMMIT --upgrade-from-assets DIR --upgrade-from-source-commit COMMIT]")
		os.Exit(2)
	}
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	supportRoot := flags.String("support-root", "deploy/support", "formal-support policy, ledgers, and evidence root")
	release := flags.String("release", "v1.2.0", "exact release ledger to verify")
	requireZeroSupported := flags.Bool("require-zero-supported", false, "fail unless every matrix cell remains candidate")
	releaseAssets := flags.String("release-assets", "", "directory containing both immutable management release tarballs")
	sourceCommit := flags.String("source-commit", "", "trusted 40-character source commit for the release tag")
	upgradeFromAssets := flags.String("upgrade-from-assets", "", "directory containing both immutable predecessor management release tarballs")
	upgradeFromSourceCommit := flags.String("upgrade-from-source-commit", "", "trusted 40-character source commit for the predecessor release tag")
	if err := flags.Parse(os.Args[2:]); err != nil {
		os.Exit(2)
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "probe-support-gate: positional arguments are not accepted")
		os.Exit(2)
	}
	if (*releaseAssets == "") != (*sourceCommit == "") {
		fmt.Fprintln(os.Stderr, "probe-support-gate: --release-assets and --source-commit must be provided together")
		os.Exit(2)
	}
	if (*upgradeFromAssets == "") != (*upgradeFromSourceCommit == "") {
		fmt.Fprintln(os.Stderr, "probe-support-gate: --upgrade-from-assets and --upgrade-from-source-commit must be provided together")
		os.Exit(2)
	}
	if *upgradeFromAssets != "" && *releaseAssets == "" {
		fmt.Fprintln(os.Stderr, "probe-support-gate: upgrade-from trusted release subject requires the target trusted release subject")
		os.Exit(2)
	}
	summary, err := supportevidence.VerifyDirectory(*supportRoot, *release, supportevidence.VerifyOptions{
		RequireZeroSupported:    *requireZeroSupported,
		ReleaseAssetsDir:        *releaseAssets,
		SourceCommit:            *sourceCommit,
		UpgradeFromAssetsDir:    *upgradeFromAssets,
		UpgradeFromSourceCommit: *upgradeFromSourceCommit,
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
