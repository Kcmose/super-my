package setupfinalize

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type directorySpec struct {
	path     string
	mode     fs.FileMode
	identity Identity
}

type fileSpec struct {
	path     string
	contents []byte
	mode     fs.FileMode
	identity Identity
}

func ensureDirectory(spec directorySpec) error {
	if spec.path == "" || !filepath.IsAbs(spec.path) || filepath.Clean(spec.path) != spec.path {
		return errors.New("managed directory path is invalid")
	}
	if err := rejectSymlinkComponents(filepath.Dir(spec.path)); err != nil {
		return err
	}
	info, err := os.Lstat(spec.path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(spec.path, spec.mode); err != nil {
			return fmt.Errorf("create managed directory %s: %w", spec.path, err)
		}
		info, err = os.Lstat(spec.path)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("managed path is not a real directory: %s", spec.path)
	}
	if err := os.Chown(spec.path, spec.identity.UID, spec.identity.GID); err != nil {
		return fmt.Errorf("set managed directory owner %s: %w", spec.path, err)
	}
	if err := os.Chmod(spec.path, spec.mode); err != nil {
		return fmt.Errorf("set managed directory permissions %s: %w", spec.path, err)
	}
	return nil
}

func createFileAtomic(spec fileSpec) error {
	if spec.path == "" || !filepath.IsAbs(spec.path) || filepath.Clean(spec.path) != spec.path {
		return errors.New("managed file path is invalid")
	}
	if err := rejectSymlinkComponents(filepath.Dir(spec.path)); err != nil {
		return err
	}
	if _, err := os.Lstat(spec.path); err == nil {
		return fmt.Errorf("managed file already exists: %s", spec.path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect managed file %s: %w", spec.path, err)
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return errors.New("generate managed temporary file name")
	}
	temporary := filepath.Join(filepath.Dir(spec.path), "."+filepath.Base(spec.path)+"."+hex.EncodeToString(random)+".tmp")
	clear(random)
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create managed temporary file: %w", err)
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chown(spec.identity.UID, spec.identity.GID); err != nil {
		return fmt.Errorf("set managed file owner: %w", err)
	}
	if err := file.Chmod(spec.mode); err != nil {
		return fmt.Errorf("set managed file permissions: %w", err)
	}
	if _, err := file.Write(spec.contents); err != nil {
		return fmt.Errorf("write managed file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync managed file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close managed file: %w", err)
	}
	if err := os.Link(temporary, spec.path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("managed file was created concurrently: %s", spec.path)
		}
		return fmt.Errorf("install managed file: %w", err)
	}
	if err := os.Remove(temporary); err != nil {
		_ = os.Remove(spec.path)
		return fmt.Errorf("remove managed temporary file: %w", err)
	}
	keep = true
	return syncDirectory(filepath.Dir(spec.path))
}

func createAbsoluteSymlink(target, link string) error {
	if !filepath.IsAbs(target) || !filepath.IsAbs(link) || filepath.Clean(target) != target || filepath.Clean(link) != link {
		return errors.New("certificate symlink paths must be absolute and canonical")
	}
	if err := rejectSymlinkComponents(filepath.Dir(link)); err != nil {
		return err
	}
	if _, err := os.Lstat(link); err == nil {
		return fmt.Errorf("certificate link already exists: %s", link)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect certificate link: %w", err)
	}
	if err := os.Symlink(target, link); err != nil {
		return fmt.Errorf("create certificate link: %w", err)
	}
	return syncDirectory(filepath.Dir(link))
}

func readSmallRegular(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 1 || info.Size() > maximum {
		return nil, fmt.Errorf("release source file is missing or invalid: %s", path)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read release source file: %w", err)
	}
	return contents, nil
}

func rejectSymlinkComponents(path string) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return errors.New("managed parent path must be absolute")
	}
	current := string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect managed path component %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("managed path component is not a real directory: %s", current)
		}
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
