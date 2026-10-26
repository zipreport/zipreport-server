// Package browser downloads a pinned Chrome for Testing build for every supported
// platform, replacing go-rod's built-in (older, revision-pinned) browser downloader.
package browser

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

// Version is the pinned Chrome for Testing build. To bump it, use the Stable
// entry of https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions.json
// The SHA-256 hashes in checksums MUST be updated together with Version.
const Version = "156.0.8078.47"

// maxDownloadBytes caps the Chrome archive (about 200 MB) before it is hashed.
const maxDownloadBytes = 512 << 20

// platforms maps GOOS_GOARCH to the Chrome for Testing platform name.
var platforms = map[string]string{
	"darwin_amd64":  "mac-x64",
	"darwin_arm64":  "mac-arm64",
	"linux_amd64":   "linux64",
	"linux_arm64":   "linux-arm64",
	"windows_386":   "win32",
	"windows_amd64": "win64",
}

// checksums maps GOOS_GOARCH (same keys as platforms) to the SHA-256 of the
// Chrome for Testing zip for Version. Google publishes no checksums, so these
// were computed from a download whose MD5 matched the x-goog-hash header.
var checksums = map[string]string{
	"darwin_amd64":  "5a38b8e3d621514fff0775dcae678a152ce378890188c88eef79fc97aa59fccd",
	"darwin_arm64":  "ad7f2b49a4dc679af286b1b1b386670d93ca7868079f69012d21ba0ac8293df3",
	"linux_amd64":   "e6c94490f0b6b337225aeb2d2a5034858516b7017356980ba94f5176d8774a50",
	"linux_arm64":   "9e37000e3ee2731e45e475a6a2eac587d3c0ee5f8aef7cab5211ecd52cf3fb7b",
	"windows_386":   "b953cca3ad09df2cde6260c3ce57265b8a166dcecb01dea3425cd6510d756a3a",
	"windows_amd64": "a4e0d57245e167e98a9d465af1e3774d2b4ec9f04e483d19bcd97a6f63cf46dd",
}

// ChecksumFor returns the pinned SHA-256 of the Chrome for Testing zip for a platform.
func ChecksumFor(goos, goarch string) (string, error) {
	c, ok := checksums[goos+"_"+goarch]
	if !ok {
		return "", fmt.Errorf("no pinned checksum for platform: %s/%s", goos, goarch)
	}
	return c, nil
}

// VerifySHA256 returns an error unless the hex SHA-256 digest got equals want.
func VerifySHA256(got, want string) error {
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, want)
	}
	return nil
}

// VerifyBytes checks data against an expected hex SHA-256.
func VerifyBytes(data []byte, want string) error {
	sum := sha256.Sum256(data)
	return VerifySHA256(hex.EncodeToString(sum[:]), want)
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

	want, err := ChecksumFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}

	fmt.Printf("Downloading Chrome for Testing from %s\n", url)

	// Download to temp file
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 60 * time.Second
	// no overall timeout: the archive is ~200 MB and slow links must not fail; CopyLimited detects stalls
	client := &http.Client{Transport: transport}
	resp, err := client.Get(url)
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

	hasher := sha256.New()
	written, err := CopyLimited(io.MultiWriter(tmpFile, hasher), resp.Body, maxDownloadBytes, 2*time.Minute)
	if err != nil {
		_ = tmpFile.Close()
		return "", fmt.Errorf("download failed: %w", err)
	}
	_ = tmpFile.Close()

	if err := VerifySHA256(hex.EncodeToString(hasher.Sum(nil)), want); err != nil {
		return "", fmt.Errorf("download verification failed: %w", err)
	}

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

// CopyLimited copies src to dst, reading at most limit bytes: it fails with "download exceeds <limit>
// bytes" if more arrive. If no data arrives for stall, src is closed and the copy fails with "download
// stalled". It closes src only on a stall; the caller still closes it.
func CopyLimited(dst io.Writer, src io.ReadCloser, limit int64, stall time.Duration) (int64, error) {
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	var stalled atomic.Bool
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(max(stall/4, time.Millisecond))
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if time.Since(time.Unix(0, last.Load())) >= stall {
					stalled.Store(true)
					_ = src.Close()
					return
				}
			}
		}
	}()

	n, err := io.Copy(dst, io.LimitReader(&progressReader{r: src, last: &last}, limit+1))
	if stalled.Load() {
		return n, errors.New("download stalled")
	}
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, fmt.Errorf("download exceeds %d bytes", limit)
	}
	return n, nil
}

type progressReader struct {
	r    io.Reader
	last *atomic.Int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.last.Store(time.Now().UnixNano())
	}
	return n, err
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

	type link struct{ target, linkname string }
	var links []link

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

		if f.Mode()&os.ModeSymlink != 0 {
			linkname, err := readSymlinkTarget(f)
			if err != nil {
				return err
			}
			// Skip links that are absolute or climb out with ".." (silently, like the zip-slip case):
			// without ".." a relative link can only point deeper into the tree, chains included.
			// ".." is looked for before cleaning: Clean would fold "a/L/../x" into "a/x" and hide it.
			if linkname == "" || filepath.IsAbs(filepath.FromSlash(linkname)) || strings.HasPrefix(linkname, "/") || hasDotDot(linkname) {
				continue
			}
			links = append(links, link{target: target, linkname: linkname})
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

	// Links go in last, so no regular entry is ever written through one.
	for _, l := range links {
		if err := os.MkdirAll(filepath.Dir(l.target), 0755); err != nil {
			return err
		}
		if err := os.Symlink(filepath.FromSlash(l.linkname), l.target); err != nil {
			return err
		}
	}

	return nil
}

func readSymlinkTarget(f *zip.File) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, 4096+1))
	if err != nil {
		return "", err
	}
	if len(b) > 4096 {
		return "", nil
	}
	return string(b), nil
}

func hasDotDot(p string) bool {
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return true
		}
	}
	return false
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

// ExtractZip extracts a zip archive to destDir, stripping a shared top-level directory.
func ExtractZip(zipPath, destDir string) error { return extractZip(zipPath, destDir) }
