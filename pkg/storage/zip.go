package storage

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const defaultDirMode = 0755

func unzipFromReader(src io.ReaderAt, size int64, dest_path string) error {
	r, err := zip.NewReader(src, size)
	if err != nil {
		return err
	}

	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()

		fpath := filepath.Join(dest_path, f.Name)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(fpath, defaultDirMode); err != nil {
				return err
			}
		} else {
			var fdir string
			if lastIndex := strings.LastIndex(fpath, string(os.PathSeparator)); lastIndex > -1 {
				fdir = fpath[:lastIndex]
			}

			err = os.MkdirAll(fdir, defaultDirMode)
			if err != nil {
				return err
			}
			f, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
			if err != nil {
				return err
			}
			defer f.Close()

			if _, err = io.Copy(f, rc); err != nil {
				return err
			}
		}
	}
	return nil
}
