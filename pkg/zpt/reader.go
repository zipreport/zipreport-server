package zpt

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// MaxFileSize caps the decompressed size of a single zip entry, guarding
// against zip-bomb entries that would otherwise exhaust memory.
const MaxFileSize = 128 << 20 // 128 MiB

type ZptReader struct {
	Reader *zip.Reader
}

func NewZptReader(r io.ReaderAt, size int64) (*ZptReader, error) {
	z := new(ZptReader)
	if err := z.Init(r, size); err != nil {
		return nil, err
	}
	return z, nil
}

func (z *ZptReader) Init(src io.ReaderAt, size int64) error {
	var err error
	z.Reader, err = zip.NewReader(src, size)
	return err
}

func (z *ZptReader) ReadFile(name string) ([]byte, error) {
	f, err := z.Reader.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	// Read at most MaxFileSize+1 so an oversized entry can be detected.
	buf, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > MaxFileSize {
		return nil, fmt.Errorf("file %q exceeds maximum decompressed size of %d bytes", name, MaxFileSize)
	}
	return buf, nil
}

var ErrFileTooLarge = errors.New("zip entry exceeds maximum decompressed size")

// OpenFile opens an entry for streaming. The declared size is checked before any
// decompression; archive/zip refuses to read past the declared size.
func (z *ZptReader) OpenFile(name string) (io.ReadCloser, int64, error) {
	f, err := z.Reader.Open(name)
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	if st.IsDir() {
		_ = f.Close()
		return nil, 0, fs.ErrNotExist
	}
	if st.Size() < 0 || st.Size() > MaxFileSize {
		_ = f.Close()
		return nil, 0, ErrFileTooLarge
	}
	return f, st.Size(), nil
}

func (z *ZptReader) Destroy() {
	z.Reader = nil
}

// NewZptReaderFromFile creates a ZptReader from a file path (helper for tests)
func NewZptReaderFromFile(path string) (*ZptReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	stat, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return NewZptReader(f, stat.Size())
}
