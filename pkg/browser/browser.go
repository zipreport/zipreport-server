// Package browser provides Chromium download functionality with arm64 Linux support.
// This replaces go-rod's built-in browser downloader which lacks arm64 Linux builds.
// The downloaded browser is stored in rod's expected cache location so rod's launcher
// finds it without additional configuration.
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

// Revision constants matching rod's expected cache directory structure.
// Rod looks for the browser at ~/.cache/rod/browser/chromium-{Revision}/chrome
const Revision = 1321438

// RevisionPlaywright is the Playwright CDN revision for arm64 Linux builds.
const RevisionPlaywright = 1202

// platformConfig maps OS/arch to download URL parameters.
type platformConfig struct {
	urlPrefix string // Google/NPM CDN path prefix
	zipName   string // ZIP filename on Google/NPM CDN
}

var platforms = map[string]platformConfig{
	"darwin_amd64":  {"Mac", "chrome-mac.zip"},
	"darwin_arm64":  {"Mac_Arm", "chrome-mac.zip"},
	"linux_amd64":   {"Linux_x64", "chrome-linux.zip"},
	"linux_arm64":   {"", ""}, // handled via Playwright CDN
	"windows_386":   {"Win", "chrome-win.zip"},
	"windows_amd64": {"Win_x64", "chrome-win.zip"},
}

// DownloadURL returns the Chromium download URL for the current platform.
func DownloadURL() (string, error) {
	key := runtime.GOOS + "_" + runtime.GOARCH
	conf, ok := platforms[key]
	if !ok {
		return "", fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	// arm64 Linux uses Playwright CDN
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
		return fmt.Sprintf(
			"https://playwright.azureedge.net/builds/chromium/%d/chromium-linux-arm64.zip",
			RevisionPlaywright,
		), nil
	}

	return fmt.Sprintf(
		"https://storage.googleapis.com/chromium-browser-snapshots/%s/%d/%s",
		conf.urlPrefix,
		Revision,
		conf.zipName,
	), nil
}

// CacheDir returns the rod-compatible browser cache directory.
func CacheDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "rod", "browser")
}

// BrowserDir returns the specific revision directory.
func BrowserDir() string {
	return filepath.Join(CacheDir(), fmt.Sprintf("chromium-%d", Revision))
}

// BinPath returns the expected browser executable path.
func BinPath() string {
	bin := "chrome"
	switch runtime.GOOS {
	case "darwin":
		bin = "Chromium.app/Contents/MacOS/Chromium"
	case "windows":
		bin = "chrome.exe"
	}
	return filepath.Join(BrowserDir(), bin)
}

// IsInstalled checks if the browser binary exists and is executable.
func IsInstalled() bool {
	info, err := os.Stat(BinPath())
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// Download fetches and installs the Chromium browser for the current platform.
// It stores the binary in rod's expected cache location.
func Download() (string, error) {
	if IsInstalled() {
		return BinPath(), nil
	}

	url, err := DownloadURL()
	if err != nil {
		return "", err
	}

	fmt.Printf("Downloading Chromium from %s\n", url)

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

	// Clean up existing directory
	destDir := BrowserDir()
	_ = os.RemoveAll(destDir)

	// Extract zip
	if err := extractZip(tmpPath, destDir); err != nil {
		return "", fmt.Errorf("extraction failed: %w", err)
	}

	binPath := BinPath()
	if err := os.Chmod(binPath, 0755); err != nil {
		return "", fmt.Errorf("chmod failed: %w", err)
	}

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
