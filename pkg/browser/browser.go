// Package browser downloads a pinned Chrome for Testing build for every supported
// platform, replacing go-rod's built-in (older, revision-pinned) browser downloader.
package browser

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Version is the pinned Chrome for Testing build. To bump it, use the Stable
// entry of https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions.json
const Version = "156.0.8078.47"

// platforms maps GOOS_GOARCH to the Chrome for Testing platform name.
var platforms = map[string]string{
	"darwin_amd64":  "mac-x64",
	"darwin_arm64":  "mac-arm64",
	"linux_amd64":   "linux64",
	"linux_arm64":   "linux-arm64",
	"windows_386":   "win32",
	"windows_amd64": "win64",
}

// DownloadURLFor returns the Chrome for Testing download URL for a platform.
func DownloadURLFor(goos, goarch string) (string, error) {
	p, ok := platforms[goos+"_"+goarch]
	if !ok {
		return "", fmt.Errorf("unsupported platform: %s/%s", goos, goarch)
	}
	return fmt.Sprintf(
		"https://storage.googleapis.com/chrome-for-testing-public/%s/%s/chrome-%s.zip",
		Version, p, p,
	), nil
}

// DownloadURL returns the Chrome for Testing download URL for the current platform.
func DownloadURL() (string, error) {
	return DownloadURLFor(runtime.GOOS, runtime.GOARCH)
}

// CacheDir returns the browser cache directory.
func CacheDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "rod", "browser")
}

// BrowserDir returns the install directory, named after the pinned Version so
// a version bump never reuses a stale binary.
func BrowserDir() string {
	return filepath.Join(CacheDir(), "chrome-"+Version)
}

// binName returns the executable path relative to the install directory.
func binName() string {
	switch runtime.GOOS {
	case "darwin":
		return "Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"
	case "windows":
		return "chrome.exe"
	}
	return "chrome"
}

// BinPath returns the expected browser executable path.
func BinPath() string {
	return filepath.Join(BrowserDir(), binName())
}

// IsInstalled checks if the browser binary exists and is executable.
func IsInstalled() bool {
	info, err := os.Stat(BinPath())
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// Download fetches and installs the pinned Chrome for Testing build for the current platform.
func Download() (string, error) {
	if IsInstalled() {
		return BinPath(), nil
	}

	url, err := DownloadURL()
	if err != nil {
		return "", err
	}

	fmt.Printf("Downloading Chrome for Testing from %s\n", url)

	// Download to temp file
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp("", "chromium-*.zip")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	written, err := io.Copy(tmpFile, resp.Body)
	if err != nil {
		_ = tmpFile.Close()
		return "", fmt.Errorf("download failed: %w", err)
	}
	_ = tmpFile.Close()

	fmt.Printf("Downloaded %d bytes\n", written)

	// Extract into a temp dir under the cache dir, then rename into place, so an
	// interrupted or concurrent install is never accepted as complete.
	if err := os.MkdirAll(CacheDir(), 0755); err != nil {
		return "", fmt.Errorf("failed to create cache dir: %w", err)
	}
	tmpDir, err := os.MkdirTemp(CacheDir(), ".chrome-download-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir: %w", err)
	}

	if err := extractZip(tmpPath, tmpDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", fmt.Errorf("extraction failed: %w", err)
	}

	if err := os.Chmod(filepath.Join(tmpDir, binName()), 0755); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", fmt.Errorf("chmod failed: %w", err)
	}

	destDir := BrowserDir()
	if !IsInstalled() {
		_ = os.RemoveAll(destDir)
	}
	if err := os.Rename(tmpDir, destDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		if IsInstalled() {
			return BinPath(), nil
		}
		return "", fmt.Errorf("install failed: %w", err)
	}

	binPath := BinPath()
	fmt.Printf("Installed: %s\n", binPath)
	return binPath, nil
}

// extractZip extracts a zip archive to the destination directory.
// It strips the top-level directory from the archive if all files share one.
func extractZip(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()

	// Determine common prefix to strip (zip archives often have a top-level dir)
	prefix := findCommonPrefix(r.File)

	for _, f := range r.File {
		name := strings.TrimPrefix(f.Name, prefix)
		if name == "" {
			continue
		}

		target := filepath.Join(destDir, filepath.FromSlash(name))

		// Prevent zip slip
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			continue
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}

		if err := extractFile(f, target); err != nil {
			return err
		}
	}

	return nil
}

func extractFile(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()

	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	_, err = io.Copy(out, rc)
	return err
}

// findCommonPrefix finds the common top-level directory in a zip archive.
func findCommonPrefix(files []*zip.File) string {
	if len(files) == 0 {
		return ""
	}

	// Find the first directory entry or the directory of the first file
	var prefix string
	for _, f := range files {
		parts := strings.SplitN(f.Name, "/", 2)
		if len(parts) < 2 {
			return "" // file at root level, no common prefix
		}
		if prefix == "" {
			prefix = parts[0] + "/"
		} else if parts[0]+"/" != prefix {
			return "" // different top-level dirs
		}
	}

	return prefix
}
