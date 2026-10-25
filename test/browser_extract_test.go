package test

import (
	"archive/zip"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"zipreport-server/pkg/browser"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type zipEntry struct {
	name   string
	body   string
	symlnk bool
}

func buildExtractZip(t *testing.T, entries []zipEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "in.zip")
	f, err := os.Create(path)
	require.NoError(t, err)
	zw := zip.NewWriter(f)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		switch {
		case e.symlnk:
			h.SetMode(os.ModeSymlink | 0777)
		case e.name[len(e.name)-1] == '/':
			h.SetMode(os.ModeDir | 0755)
		default:
			h.SetMode(0644)
		}
		w, err := zw.CreateHeader(h)
		require.NoError(t, err)
		_, err = w.Write([]byte(e.body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
	return path
}

func TestExtractZip_RelativeSymlink(t *testing.T) {
	zp := buildExtractZip(t, []zipEntry{
		{name: "dir/"},
		{name: "dir/file", body: "data"},
		{name: "link", body: "dir/file", symlnk: true},
	})
	dest := filepath.Join(t.TempDir(), "dest")
	require.NoError(t, os.MkdirAll(dest, 0755))
	require.NoError(t, browser.ExtractZip(zp, dest))

	got, err := os.Readlink(filepath.Join(dest, "link"))
	require.NoError(t, err)
	assert.Equal(t, "dir/file", got)
	b, err := os.ReadFile(filepath.Join(dest, "link"))
	require.NoError(t, err)
	assert.Equal(t, "data", string(b))
}

func TestExtractZip_RejectsUnsafeSymlinks(t *testing.T) {
	cases := map[string][]zipEntry{
		"absolute": {
			{name: "abs", body: "/etc/passwd", symlnk: true},
			{name: "keep", body: "x"},
		},
		"dotdot": {
			{name: "up", body: "../../x", symlnk: true},
			{name: "keep", body: "x"},
		},
		"chained": {
			{name: "a/b/L", body: "../../q", symlnk: true},
			{name: "M", body: "a/b/L/../../outside", symlnk: true},
			{name: "M/evil", body: "pwned"},
			{name: "keep", body: "x"},
		},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dest := filepath.Join(parent, "dest")
			require.NoError(t, os.MkdirAll(dest, 0755))
			require.NoError(t, browser.ExtractZip(buildExtractZip(t, entries), dest))

			for _, e := range entries {
				if !e.symlnk {
					continue
				}
				fi, err := os.Lstat(filepath.Join(dest, e.name))
				if err == nil {
					assert.Zero(t, fi.Mode()&os.ModeSymlink, "%s must not become a symlink", e.name)
				}
			}
			_, err := os.Stat(filepath.Join(dest, "keep"))
			require.NoError(t, err, "safe entries are still extracted")

			require.NoError(t, filepath.WalkDir(parent, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if p == parent || p == dest || isUnder(p, dest) {
					return nil
				}
				t.Errorf("unexpected path outside the destination: %s", p)
				return nil
			}))
		})
	}
}

func isUnder(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
