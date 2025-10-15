package zpt

import (
	"archive/zip"
	"io"
	"os"
)

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
	defer f.Close()
	return io.ReadAll(f)
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
		f.Close()
		return nil, err
	}
	return NewZptReader(f, stat.Size())
}
