package test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
	"zipreport-server/pkg/browser"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBrowserChecksum_EveryPlatformPinned(t *testing.T) {
	for _, p := range [][2]string{
		{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"},
		{"linux", "arm64"}, {"windows", "386"}, {"windows", "amd64"},
	} {
		_, err := browser.DownloadURLFor(p[0], p[1])
		require.NoError(t, err)
		sum, err := browser.ChecksumFor(p[0], p[1])
		require.NoError(t, err, "%s/%s", p[0], p[1])
		assert.Regexp(t, `^[0-9a-f]{64}$`, sum, "%s/%s", p[0], p[1])
	}
	_, err := browser.ChecksumFor("plan9", "amd64")
	assert.Error(t, err)
}

func TestBrowserChecksum_VerifyBytes(t *testing.T) {
	data := []byte("chrome")
	sum := sha256.Sum256(data)
	good := hex.EncodeToString(sum[:])

	assert.NoError(t, browser.VerifyBytes(data, good))
	assert.Error(t, browser.VerifyBytes([]byte("chrom3"), good))

	bad := []byte(good)
	if bad[0] == '0' {
		bad[0] = '1'
	} else {
		bad[0] = '0'
	}
	assert.ErrorContains(t, browser.VerifyBytes(data, string(bad)), "checksum mismatch")
}

// Downloads the real pinned build into an empty HOME; it installs only if the
// pinned SHA-256 matches.
func TestBrowserChecksum_PinnedDownloadVerifies(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network download in short mode")
	}
	t.Setenv("HOME", t.TempDir())
	bin, err := browser.Download()
	require.NoError(t, err)
	_, err = os.Stat(bin)
	assert.NoError(t, err)
}
