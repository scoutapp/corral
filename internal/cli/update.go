package cli

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/scoutapp/corral/internal/config"
	"github.com/scoutapp/corral/internal/release"
)

// cmdUpdate self-updates corral: it resolves the newest published release,
// and (unless --check) downloads the platform binary + asset bundle, replaces the
// running executable, refreshes ~/.corral/assets, and rebuilds the sandbox
// image so it's re-stamped with the new version.
//
// This replaces the OLD `corral update`, which edited the PROJECT config —
// that behavior now lives under `corral config`.
//
// Release source: config.UpdateRepo() (default scoutapp/corral, overridable
// in ~/.corral/global-settings.json). Everything is fetched anonymously from
// GitHub Releases — same artifacts and layout the curl|bash installer uses.
//
// Flags:
//
//	--check              report current/latest/whether an update is available, then exit
//	--yes / -y           skip the "proceed?" confirmation
//	--repo <owner/name>  check/update against this repo for this run only (not saved)
//	--set-repo <o/n>     persist the update source to global settings, then continue
func cmdUpdate(args []string) error {
	checkOnly := false
	assumeYes := false
	repoOverride := ""
	setRepo := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--check":
			checkOnly = true
		case "--yes", "-y":
			assumeYes = true
		case "--repo":
			if i+1 >= len(args) {
				return fmt.Errorf("--repo needs an owner/name argument")
			}
			i++
			repoOverride = args[i]
		case "--set-repo":
			if i+1 >= len(args) {
				return fmt.Errorf("--set-repo needs an owner/name argument")
			}
			i++
			setRepo = args[i]
		default:
			return fmt.Errorf("unknown flag for update: %s", a)
		}
	}

	// Persist a new update source if requested (before resolving the repo below).
	if setRepo != "" {
		if _, err := validateRepoInput(setRepo); err != nil {
			return err
		}
		gs := config.ReadGlobalSettings()
		gs.UpdateRepo = setRepo
		if err := config.WriteGlobalSettings(gs); err != nil {
			return fmt.Errorf("could not save update source: %w", err)
		}
		fmt.Printf("Update source set to %s\n", gs.UpdateRepoOrDefault())
	}

	repo := config.UpdateRepo()
	if repoOverride != "" {
		norm, err := validateRepoInput(repoOverride)
		if err != nil {
			return err
		}
		repo = norm
	}
	current := config.Version
	baseURL := config.RepoToBaseURL(repo)

	latest, err := release.LatestTag(baseURL)
	if err != nil {
		// Non-fatal for the user: the source may be private/unreachable or offline.
		// Report it plainly rather than a stack-y error.
		fmt.Printf("Couldn't reach the update source %s.\n", repo)
		fmt.Printf("  (%v)\n", err)
		fmt.Println("  If this repo is private or you use a custom source, set one with:")
		fmt.Println("    corral update --set-repo owner/name          (a GitHub repo)")
		fmt.Println("    corral update --set-repo https://host/owner/repo   (a non-GitHub host)")
		return nil
	}

	newer := release.IsNewer(latest, current)
	fmt.Printf("corral %s (installed)\n", current)
	fmt.Printf("corral %s (latest, from %s)\n", latest, repo)

	if !newer {
		fmt.Println("✅ You're on the latest release.")
		return nil
	}
	fmt.Printf("⬆️  An update is available: %s → %s\n", current, latest)

	if checkOnly {
		return nil
	}

	if !assumeYes {
		if !config.AskYesNo(fmt.Sprintf("Update corral to %s now?", latest)) {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	return runSelfUpdate(baseURL, latest)
}

// validateRepoInput normalizes and validates a user-supplied update source,
// returning either a GitHub "owner/name" or a full base URL, or a friendly error.
func validateRepoInput(s string) (string, error) {
	norm, ok := config.NormalizeRepo(s)
	if !ok {
		return "", fmt.Errorf("%q doesn't look like a GitHub owner/name (e.g. scoutapp/corral) or a release URL (e.g. https://host/owner/repo)", s)
	}
	return norm, nil
}

// runSelfUpdate performs the actual download → verify → replace-binary →
// sync-assets → rebuild-image sequence for the given release base URL + tag.
func runSelfUpdate(baseURL, tag string) error {
	platform := fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH)
	base := strings.TrimRight(baseURL, "/") + "/releases/download/" + tag
	binArchive := fmt.Sprintf("corral_%s.tar.gz", platform)
	assetsArchive := "corral-assets.tar.gz"

	tmp, err := os.MkdirTemp("", "corral-update-")
	if err != nil {
		return fmt.Errorf("could not create temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	log.Printf("Downloading %s ...", binArchive)
	binPath := filepath.Join(tmp, binArchive)
	if err := downloadFile(base+"/"+binArchive, binPath); err != nil {
		return fmt.Errorf("failed to download %s (does %s ship a %s build?): %w", binArchive, tag, platform, err)
	}

	log.Printf("Downloading %s ...", assetsArchive)
	assetsPath := filepath.Join(tmp, assetsArchive)
	if err := downloadFile(base+"/"+assetsArchive, assetsPath); err != nil {
		return fmt.Errorf("failed to download %s: %w", assetsArchive, err)
	}

	// Best-effort checksum verification (mirrors install.sh): if checksums.txt is
	// present and lists our files, both must match or we abort.
	if err := verifyChecksums(base, tmp, binArchive, assetsArchive); err != nil {
		return err
	}

	// Extract the new binary.
	log.Println("Extracting binary ...")
	newBin := filepath.Join(tmp, "corral.new")
	if err := extractBinaryFromTarGz(binPath, "corral", newBin); err != nil {
		return fmt.Errorf("could not extract binary: %w", err)
	}
	if err := os.Chmod(newBin, 0o755); err != nil {
		return err
	}

	// Replace the running executable (detect-and-instruct on permission failure).
	if err := replaceRunningBinary(newBin); err != nil {
		return err
	}

	// Refresh the asset bundle (~/.corral/assets/{sandbox,host}).
	log.Println("Refreshing asset bundle ...")
	if err := syncAssetBundle(assetsPath); err != nil {
		return fmt.Errorf("could not refresh assets: %w", err)
	}

	// Rebuild the sandbox image so it's re-stamped with the new version. The
	// rebuilt binary is the one we just installed, but this process is still the
	// OLD binary — so shell out to the freshly-installed one to pick up its
	// version stamping and any build changes.
	log.Println("Rebuilding sandbox image (this can take a few minutes) ...")
	if err := rebuildViaInstalledBinary(); err != nil {
		log.Printf("⚠️  Image rebuild failed: %v", err)
		log.Println("    Run `corral rebuild` manually to finish the update.")
	}

	fmt.Println()
	fmt.Printf("✅ Updated corral to %s.\n", tag)
	fmt.Println("   If the dashboard was running, restart it to pick up the new build:")
	fmt.Println("     corral dashboard stop && corral dashboard")
	return nil
}

// replaceRunningBinary swaps the currently-running executable with newBin. It
// resolves symlinks first (so a ~/.local/bin symlink → real file updates the real
// file), then does an atomic same-directory rename. If the install directory
// isn't writable, it does NOT invoke sudo itself — it prints the exact
// `sudo install` command for the user to run (the conservative, consent-first
// path we settled on) and returns an error so the update stops cleanly.
func replaceRunningBinary(newBin string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not locate the running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)

	if !dirWritable(dir) {
		// Stage the new binary somewhere stable the user can reference, then print
		// the privileged command. We deliberately do not run sudo for them.
		staged := filepath.Join(config.CorralHome(), "corral.staged")
		if err := os.MkdirAll(config.CorralHome(), 0o755); err == nil {
			if copyErr := copyFile(newBin, staged, 0o755); copyErr == nil {
				newBin = staged
			}
		}
		return fmt.Errorf("%s is not writable by your user.\n"+
			"    The new binary is staged at:\n      %s\n"+
			"    Finish the update with:\n      sudo install -m 0755 %s %s",
			dir, newBin, newBin, exe)
	}

	// Atomic replace within the same directory: write a sibling temp then rename
	// over the target. A running executable's inode stays live, so replacing the
	// on-disk file is safe on macOS/Linux.
	tmpDest := filepath.Join(dir, ".corral.new")
	if err := copyFile(newBin, tmpDest, 0o755); err != nil {
		return fmt.Errorf("could not stage new binary in %s: %w", dir, err)
	}
	if err := os.Rename(tmpDest, exe); err != nil {
		os.Remove(tmpDest)
		return fmt.Errorf("could not replace %s: %w", exe, err)
	}
	log.Printf("Installed new binary to %s", exe)
	return nil
}

// syncAssetBundle extracts corral-assets.tar.gz (holding sandbox/ and host/)
// into ~/.corral/assets, fully replacing those two subtrees — the same
// refresh install.sh does.
func syncAssetBundle(assetsTarGz string) error {
	assetsDir := filepath.Join(config.CorralHome(), "assets")
	// Replace sandbox/ and host/ wholesale so removed files don't linger.
	for _, sub := range []string{"sandbox", "host"} {
		os.RemoveAll(filepath.Join(assetsDir, sub))
	}
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		return err
	}
	return extractTarGz(assetsTarGz, assetsDir)
}

// rebuildViaInstalledBinary shells out to the freshly-installed corral to
// rebuild the image, so the new version stamping/build logic is used rather than
// this still-running old process's.
func rebuildViaInstalledBinary() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// `rebuild` without --destroy: rebuild the image in place (see cmdRebuild).
	cmd := exec.Command(exe, "rebuild")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// --- download / archive helpers ------------------------------------------------

// downloadFile GETs url to dest with a generous timeout (release archives are a
// few MB). Non-2xx is an error.
func downloadFile(url, dest string) error {
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, url)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// verifyChecksums downloads checksums.txt (best-effort) and, if it lists our
// archives, requires their sha256 to match. A missing checksums.txt is tolerated
// (returns nil) — matching install.sh; a present-but-mismatched entry is fatal.
func verifyChecksums(base, dir string, files ...string) error {
	sumsPath := filepath.Join(dir, "checksums.txt")
	if err := downloadFile(base+"/checksums.txt", sumsPath); err != nil {
		log.Printf("⚠️  checksums.txt unavailable (%v) — skipping verification.", err)
		return nil
	}
	want, err := parseChecksums(sumsPath)
	if err != nil {
		log.Printf("⚠️  could not parse checksums.txt (%v) — skipping verification.", err)
		return nil
	}
	log.Println("Verifying checksums ...")
	for _, name := range files {
		expected, ok := want[name]
		if !ok {
			continue // not listed — nothing to check for this file
		}
		got, err := sha256File(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("could not hash %s: %w", name, err)
		}
		if !strings.EqualFold(got, expected) {
			return fmt.Errorf("checksum mismatch for %s (expected %s, got %s) — aborting", name, expected, got)
		}
	}
	return nil
}

// parseChecksums reads a `<sha256>  <filename>` file into a name→hash map.
func parseChecksums(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		out[filepath.Base(fields[1])] = fields[0]
	}
	return out, nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractBinaryFromTarGz pulls a single named file out of a .tar.gz to dest.
func extractBinaryFromTarGz(tarGz, name, dest string) error {
	f, err := os.Open(tarGz)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%q not found in archive", name)
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) != name || hdr.Typeflag != tar.TypeReg {
			continue
		}
		out, err := os.Create(dest)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, tr)
		return err
	}
}

// extractTarGz extracts an entire .tar.gz into destDir, preserving the archive's
// directory structure. It guards against path traversal (entries escaping
// destDir) and preserves regular files, dirs, and symlinks.
func extractTarGz(tarGz, destDir string) error {
	f, err := os.Open(tarGz)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	destAbs, err := filepath.Abs(destDir)
	if err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(destDir, hdr.Name)
		// Reject entries that would escape destDir.
		if rel, err := filepath.Rel(destAbs, target); err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}
}

// copyFile copies src to dst with the given mode (dst is truncated/created).
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
