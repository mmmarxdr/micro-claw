package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/creativeprojects/go-selfupdate"
)

// GitHub coordinates for the canonical Daimon repo. Used to discover releases.
const (
	repoOwner = "mmmarxdr"
	repoName  = "daimon"
)

// runUpdateCommand implements `daimon update`.
//
// Flags:
//
//	--check         only report whether a newer version exists; do not install
//	--version vX.Y  install a specific version (rollback or pin)
//	--force         reinstall even when current version is already latest
func runUpdateCommand(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	checkOnly := fs.Bool("check", false, "only check for a newer version, do not install")
	targetVersion := fs.String("version", "", "install a specific version tag (e.g. v0.9.1)")
	force := fs.Bool("force", false, "reinstall even if already on the latest version")
	if err := fs.Parse(args); err != nil {
		printUpdateUsage()
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	source, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{})
	if err != nil {
		return fmt.Errorf("init github source: %w", err)
	}
	updater, err := selfupdate.NewUpdater(selfupdate.Config{Source: source})
	if err != nil {
		return fmt.Errorf("init updater: %w", err)
	}
	repo := selfupdate.NewRepositorySlug(repoOwner, repoName)

	var release *selfupdate.Release
	var found bool
	if *targetVersion != "" {
		release, found, err = updater.DetectVersion(ctx, repo, *targetVersion)
	} else {
		release, found, err = updater.DetectLatest(ctx, repo)
	}
	if err != nil {
		return fmt.Errorf("query GitHub releases: %w", err)
	}
	if !found {
		if *targetVersion != "" {
			return fmt.Errorf("no release found matching version %q for %s/%s", *targetVersion, runtime.GOOS, runtime.GOARCH)
		}
		return fmt.Errorf("no releases found for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	current := version
	latest := release.Version()
	fmt.Printf("Current: %s\nLatest:  %s\n", current, latest)

	// Dev builds have no semver to compare; report and exit early. --check is
	// always safe (read-only); a real update is refused unless --version is
	// given (explicit opt-in).
	if isDevBuild() && *targetVersion == "" {
		if *checkOnly {
			fmt.Println("Development build — version comparison skipped. Latest release shown above.")
			return nil
		}
		return fmt.Errorf(
			"refusing to replace development build (version=%q). " +
				"Run `go install github.com/mmmarxdr/daimon/cmd/daimon@latest` " +
				"or pass `--version vX.Y.Z` to pin a release",
			version,
		)
	}

	if *checkOnly {
		if !*force && release.LessOrEqual(current) {
			fmt.Println("Already on the latest version.")
			return nil
		}
		fmt.Println("A newer version is available. Run `daimon update` to install.")
		return nil
	}

	if !*force && *targetVersion == "" && release.LessOrEqual(current) {
		fmt.Println("Already on the latest version. Use --force to reinstall.")
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current executable: %w", err)
	}

	// Pre-flight: confirm we can write to the binary's directory. Without this
	// check, the user sees a generic permission error mid-download.
	if err := checkWritable(exe); err != nil {
		return fmt.Errorf(
			"%w\nHint: re-run with sudo, or move %q to a user-writable directory like ~/.local/bin",
			err, exe,
		)
	}

	fmt.Printf("Downloading %s for %s/%s...\n", latest, runtime.GOOS, runtime.GOARCH)
	if err := updater.UpdateTo(ctx, release, exe); err != nil {
		return fmt.Errorf("install update: %w", err)
	}

	fmt.Printf("Updated %s -> %s\n", current, latest)
	if notes := release.ReleaseNotes; notes != "" {
		fmt.Printf("\nRelease notes:\n%s\n", notes)
	}
	return nil
}

// checkWritable verifies the directory containing path is writable by the
// current user. This catches the common /usr/local/bin case before we waste a
// download.
func checkWritable(path string) error {
	dir := path
	if i := lastSlash(path); i >= 0 {
		dir = path[:i]
	}
	probe, err := os.CreateTemp(dir, ".daimon-update-probe-*")
	if err != nil {
		return fmt.Errorf("not writable: %s (%w)", dir, err)
	}
	probe.Close()
	os.Remove(probe.Name())
	return nil
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' || s[i] == '\\' {
			return i
		}
	}
	return -1
}

func printUpdateUsage() {
	fmt.Fprintln(os.Stderr, `Usage: daimon update [--check] [--version vX.Y.Z] [--force]

Flags:
  --check               Only report whether a newer version exists; do not install.
  --version vX.Y.Z      Install a specific release tag (rollback or pin).
  --force               Reinstall even if already on the latest version.`)
}
