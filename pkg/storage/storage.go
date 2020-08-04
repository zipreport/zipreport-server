package storage

import (
	"errors"
	"github.com/rs/xid"
	"io"
	"os"
	"path/filepath"
)

var ErrPathNotFound = errors.New("Storage path not found")
var ErrPathNotDir = errors.New("Storage path is not a directory")
var ErrPathReadOnly = errors.New("Error writing to storage path")
var ErrInvalidName = errors.New("Invalid name")

type Backend interface {
	Init() error
	NewTmpFolder() (string, error)
	RemoveTmpFolder(name string) error
	UnpackZipFromReader(src io.ReaderAt, size int64) (string, error)
	GetPath(name string) (string, error)
}

type DiskBackend struct {
	Backend
	FolderPerms os.FileMode
	path        string
}

func NewDiskBackend(path string) *DiskBackend {
	return &DiskBackend{
		FolderPerms: 0755,
		path:        path,
	}
}

func (d *DiskBackend) Init() error {
	if info, err := os.Stat(d.path); os.IsNotExist(err) {
		return ErrPathNotFound
	} else {
		if !info.IsDir() {
			return ErrPathNotDir
		}
	}
	// check if it is writable
	name, err := d.NewTmpFolder()
	if err != nil {
		return ErrPathReadOnly
	}
	return d.RemoveTmpFolder(name)
}

func (d *DiskBackend) NewTmpFolder() (string, error) {
	guid := xid.New()
	name := guid.String()
	if err := os.Mkdir(filepath.Join(d.path, name), os.ModePerm); err != nil {
		return "", err
	}
	return name, nil
}

func (d *DiskBackend) RemoveTmpFolder(name string) error {
	if 0 == len(name) {
		return ErrInvalidName
	}
	path := filepath.Join(d.path, name)
	return os.RemoveAll(path)
}

func (d *DiskBackend) UnpackZipFromReader(src io.ReaderAt, size int64) (string, error) {
	name, err := d.NewTmpFolder()
	if err != nil {
		return "", err
	}

	if err := unzipFromReader(src, size, filepath.Join(d.path, name)); err != nil {
		d.RemoveTmpFolder(name)
		return "", err
	}
	return name, nil
}

func (d *DiskBackend) GetPath(name string) (string, error) {
	return filepath.Join(d.path, name), nil
}
