package cli

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	repoOwner = "STAIxBWLB"
	repoName  = "rootfiles"
	githubAPI = "https://api.github.com"
)

type releaseInfo struct {
	TagName string `json:"tag_name"`
}

func newUpgradeCmd(currentVersion string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "update",
		Aliases: []string{"upgrade"},
		Short:   "Self-update rootfiles to the latest (or specified) version",
		RunE: func(cmd *cobra.Command, args []string) error {
			checkOnly, _ := cmd.Flags().GetBool("check")
			targetVersion, _ := cmd.Flags().GetString("version")
			dryRun, _ := cmd.Flags().GetBool("dry-run")

			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()

			// Resolve target version
			if targetVersion == "" {
				fmt.Print("Checking latest version... ")
				latest, err := fetchLatestVersion(ctx)
				if err != nil {
					return fmt.Errorf("fetching latest version: %w", err)
				}
				targetVersion = latest
				fmt.Println(targetVersion)
			}

			current := normalizeVersion(currentVersion)
			target := normalizeVersion(targetVersion)

			if current == target {
				fmt.Printf("Already at version %s, nothing to do.\n", targetVersion)
				return nil
			}
			fmt.Printf("Current: %s → Target: %s\n", currentVersion, targetVersion)

			if checkOnly {
				fmt.Println("Update available. Run `rootfiles update` to install.")
				return nil
			}

			osName := runtime.GOOS
			arch := runtime.GOARCH
			if osName != "linux" {
				return fmt.Errorf("self-update only supported on linux (current: %s)", osName)
			}

			// Download
			tmpDir, err := os.MkdirTemp("", "rootfiles-upgrade-*")
			if err != nil {
				return fmt.Errorf("creating temp dir: %w", err)
			}
			defer os.RemoveAll(tmpDir)

			fmt.Print("Downloading... ")
			archivePath, err := downloadRelease(ctx, targetVersion, osName, arch, tmpDir)
			if err != nil {
				return fmt.Errorf("download failed: %w", err)
			}
			fmt.Println("OK")

			// Verify checksum
			fmt.Print("Verifying checksum... ")
			if err := verifyChecksum(ctx, archivePath, targetVersion); err != nil {
				return fmt.Errorf("checksum verification failed: %w", err)
			}
			fmt.Println("OK")

			// Extract
			fmt.Print("Extracting... ")
			newBinary, err := extractBinary(archivePath, tmpDir)
			if err != nil {
				return fmt.Errorf("extraction failed: %w", err)
			}
			fmt.Println("OK")

			if dryRun {
				fmt.Printf("Dry-run: would replace binary with %s\n", newBinary)
				return nil
			}

			// Replace
			currentBinary, err := os.Executable()
			if err != nil {
				return fmt.Errorf("resolving current binary: %w", err)
			}
			currentBinary, err = filepath.EvalSymlinks(currentBinary)
			if err != nil {
				return fmt.Errorf("resolving symlinks: %w", err)
			}

			fmt.Printf("Replacing %s... ", currentBinary)
			if err := replaceBinary(newBinary, currentBinary); err != nil {
				return fmt.Errorf("replace failed: %w", err)
			}
			fmt.Println("OK")

			// Ensure the `root` alias symlink exists next to the binary. The
			// installer creates this on fresh installs; users upgrading from
			// pre-v0.10.0 through `rootfiles update` never hit install.sh
			// again, so without this they see `root: command not found` after
			// the upgrade. Idempotent — a no-op if the symlink is already
			// correct, and we swallow the error so a read-only /usr/local/bin
			// (rare, but possible) doesn't turn a successful upgrade into a
			// failed one.
			if link, err := ensureRootAlias(currentBinary); err == nil && link != "" {
				fmt.Printf("Symlink:  %s -> %s\n", link, filepath.Base(currentBinary))
			}

			fmt.Printf("Updated to %s\n", targetVersion)
			return nil
		},
	}
	cmd.Flags().Bool("check", false, "Only check if an update is available")
	cmd.Flags().String("version", "", "Update to a specific version (e.g., v0.9.0)")
	return cmd
}

// fetchLatestVersion queries GitHub API for the latest release tag.
func fetchLatestVersion(ctx context.Context) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", githubAPI, repoOwner, repoName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var info releaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", err
	}
	if info.TagName == "" {
		return "", fmt.Errorf("no tag_name in release response")
	}
	return info.TagName, nil
}

// downloadRelease downloads the release archive to destDir.
func downloadRelease(ctx context.Context, version, osName, arch, destDir string) (string, error) {
	ver := strings.TrimPrefix(version, "v")
	filename := fmt.Sprintf("rootfiles_%s_%s_%s.tar.gz", ver, osName, arch)
	url := fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/%s",
		repoOwner, repoName, version, filename)

	outPath := filepath.Join(destDir, filename)
	return outPath, downloadFile(ctx, url, outPath)
}

// verifyChecksum downloads checksums.txt and verifies the archive's SHA256.
func verifyChecksum(ctx context.Context, archivePath, version string) error {
	url := fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/checksums.txt",
		repoOwner, repoName, version)

	checksumData, err := fetchBody(ctx, url)
	if err != nil {
		return fmt.Errorf("downloading checksums: %w", err)
	}

	// Compute actual hash
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actualHash := hex.EncodeToString(h.Sum(nil))

	// Find expected hash
	archiveName := filepath.Base(archivePath)
	for _, line := range strings.Split(string(checksumData), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == archiveName {
			if parts[0] == actualHash {
				return nil
			}
			return fmt.Errorf("hash mismatch: expected %s, got %s", parts[0], actualHash)
		}
	}
	return fmt.Errorf("no checksum found for %s", archiveName)
}

// extractBinary extracts the rootfiles binary from tar.gz.
func extractBinary(archivePath, destDir string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if filepath.Base(hdr.Name) == "rootfiles" {
			outPath := filepath.Join(destDir, "rootfiles")
			out, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY, 0755)
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return "", err
			}
			out.Close()
			return outPath, nil
		}
	}
	return "", fmt.Errorf("rootfiles binary not found in archive")
}

// ensureRootAlias creates (or repairs) the `root` symlink next to the
// installed rootfiles binary. Returns the resulting link path (for logging)
// when a change was made, the empty string when the symlink was already
// correct or the directory is not writable.
//
// Only acts when the rootfiles binary lives under /usr/local/bin or
// /usr/bin — the two paths the installer writes to. Any other location is
// assumed to be a custom install where managing the alias is the operator's
// responsibility.
func ensureRootAlias(rootfilesPath string) (string, error) {
	dir := filepath.Dir(rootfilesPath)
	base := filepath.Base(rootfilesPath)
	if base != "rootfiles" {
		return "", nil
	}
	switch dir {
	case "/usr/local/bin", "/usr/bin":
		// accepted installer locations
	default:
		return "", nil
	}

	linkPath := filepath.Join(dir, "root")
	if target, err := os.Readlink(linkPath); err == nil && target == rootfilesPath {
		return "", nil // already correct
	}

	// Remove any stale entry (symlink with a different target, or a
	// non-symlink file of the same name) before recreating.
	_ = os.Remove(linkPath)
	if err := os.Symlink(rootfilesPath, linkPath); err != nil {
		return "", err
	}
	return linkPath, nil
}

// replaceBinary atomically swaps the current binary with the new one.
//
// Strategy: stage the new bytes to a sibling temp file in the same directory
// as currentPath, fsync, chmod 0755, then os.Rename over currentPath. rename(2)
// is atomic on the same filesystem and only swaps the directory entry — the
// running process remains mapped to the old inode via its text segment, so
// the current execution finishes cleanly while future invocations see the
// new binary.
//
// This also avoids ETXTBSY (Linux write-to-executing-binary) which would be
// triggered if we tried to O_TRUNC+write over the running executable
// directly. See dotfiles-v2 commit 5eb2a01 for the original write-style bug.
func replaceBinary(newPath, currentPath string) error {
	src, err := os.Open(newPath)
	if err != nil {
		return fmt.Errorf("opening new binary: %w", err)
	}
	defer src.Close()

	dir := filepath.Dir(currentPath)
	tmp, err := os.CreateTemp(dir, ".rootfiles.*.new")
	if err != nil {
		return fmt.Errorf("creating staging file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("writing staging file: %w", err)
	}
	if err := tmp.Chmod(0755); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod staging file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("closing staging file: %w", err)
	}
	if err := os.Rename(tmpPath, currentPath); err != nil {
		cleanup()
		return fmt.Errorf("replacing binary %s: %w", currentPath, err)
	}
	return nil
}

// normalizeVersion strips "v" prefix and any parenthetical suffix for comparison.
func normalizeVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	if idx := strings.Index(v, " "); idx != -1 {
		v = v[:idx]
	}
	return v
}

// downloadFile downloads a URL to a local path.
func downloadFile(ctx context.Context, url, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

// fetchBody downloads a URL and returns the body as bytes.
func fetchBody(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	return io.ReadAll(resp.Body)
}
