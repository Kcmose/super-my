package setup

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const privateFileMode fs.FileMode = 0o600

var ErrFileNotFound = errors.New("secure file not found")

// SecureFiles is the deliberately small persistence boundary used by the
// setup state machine. Production uses RootFileStore; tests can provide an
// in-memory implementation without weakening the production ownership rules.
type SecureFiles interface {
	Read(path string, maxBytes int64) ([]byte, error)
	CreateAtomic(path string, contents []byte) error
	WriteAtomic(path string, contents []byte) error
	Remove(path string) error
}

// RootFileStore persists root-owned 0600 files below root-owned directories.
// It never follows a target symlink and always replaces files atomically.
type RootFileStore struct {
	ownerUID uint32
	random   io.Reader
}

func NewRootFileStore() *RootFileStore {
	return &RootFileStore{ownerUID: 0, random: rand.Reader}
}

func newRootFileStoreForUID(uid uint32) *RootFileStore {
	return &RootFileStore{ownerUID: uid, random: rand.Reader}
}

func (store *RootFileStore) Read(path string, maxBytes int64) ([]byte, error) {
	if maxBytes < 1 {
		return nil, errors.New("secure file size limit must be positive")
	}
	if err := store.validatePath(path); err != nil {
		return nil, err
	}
	if err := store.validateParent(path); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrFileNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open secure file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect secure file: %w", err)
	}
	if err := store.validateFileInfo(info); err != nil {
		return nil, err
	}
	linkInfo, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, linkInfo) {
		return nil, errors.New("secure file changed while opening")
	}
	if info.Size() < 1 || info.Size() > maxBytes {
		return nil, fmt.Errorf("secure file must contain between 1 and %d bytes", maxBytes)
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read secure file: %w", err)
	}
	if int64(len(contents)) > maxBytes {
		return nil, fmt.Errorf("secure file exceeds %d bytes", maxBytes)
	}
	return contents, nil
}

func (store *RootFileStore) CreateAtomic(path string, contents []byte) error {
	return store.write(path, contents, true)
}

func (store *RootFileStore) WriteAtomic(path string, contents []byte) error {
	return store.write(path, contents, false)
}

func (store *RootFileStore) write(path string, contents []byte, exclusive bool) error {
	if len(contents) == 0 {
		return errors.New("refusing to write an empty secure file")
	}
	if err := store.validatePath(path); err != nil {
		return err
	}
	if err := store.validateParent(path); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if exclusive {
			return fs.ErrExist
		}
		if err := store.validateFileInfo(info); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect secure file target: %w", err)
	}

	suffixBytes := make([]byte, 16)
	if _, err := io.ReadFull(store.random, suffixBytes); err != nil {
		return errors.New("generate secure temporary file name")
	}
	temporary := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"."+hex.EncodeToString(suffixBytes)+".tmp")
	clear(suffixBytes)
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return fmt.Errorf("create secure temporary file: %w", err)
	}
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chown(int(store.ownerUID), int(store.ownerUID)); err != nil {
		return fmt.Errorf("set secure file owner: %w", err)
	}
	if err := file.Chmod(privateFileMode); err != nil {
		return fmt.Errorf("set secure file permissions: %w", err)
	}
	if _, err := file.Write(contents); err != nil {
		return fmt.Errorf("write secure temporary file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync secure temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close secure temporary file: %w", err)
	}
	if exclusive {
		if err := os.Link(temporary, path); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return fs.ErrExist
			}
			return fmt.Errorf("install secure file exclusively: %w", err)
		}
		if err := os.Remove(temporary); err != nil {
			_ = os.Remove(path)
			return fmt.Errorf("remove linked secure temporary file: %w", err)
		}
		removeTemporary = false
	} else {
		if err := os.Rename(temporary, path); err != nil {
			return fmt.Errorf("replace secure file atomically: %w", err)
		}
		removeTemporary = false
	}
	return syncDirectory(filepath.Dir(path))
}

func (store *RootFileStore) Remove(path string) error {
	if err := store.validatePath(path); err != nil {
		return err
	}
	if err := store.validateParent(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrFileNotFound
	}
	if err != nil {
		return fmt.Errorf("inspect secure file before removal: %w", err)
	}
	if err := store.validateFileInfo(info); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove secure file: %w", err)
	}
	return syncDirectory(filepath.Dir(path))
}

func (store *RootFileStore) validatePath(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) == "." {
		return errors.New("secure file path must be absolute and canonical")
	}
	if strings.ContainsRune(filepath.Base(path), os.PathSeparator) {
		return errors.New("secure file path is invalid")
	}
	return nil
}

func (store *RootFileStore) validateParent(path string) error {
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("inspect secure file directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("secure file directory must not be a symlink")
	}
	uid, ok := fileOwnerUID(info)
	if !ok || uid != store.ownerUID {
		return errors.New("secure file directory must have the expected owner")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("secure file directory must not be group- or world-writable")
	}
	return nil
}

func (store *RootFileStore) validateFileInfo(info fs.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("secure file must be a regular file, not a symlink")
	}
	uid, ok := fileOwnerUID(info)
	if !ok || uid != store.ownerUID {
		return errors.New("secure file must have the expected owner")
	}
	if info.Mode().Perm() != privateFileMode {
		return errors.New("secure file permissions must be 0600")
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open secure file directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync secure file directory: %w", err)
	}
	return nil
}
