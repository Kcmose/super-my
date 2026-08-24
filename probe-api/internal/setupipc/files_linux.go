//go:build linux

package setupipc

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const (
	privateDirectoryMode = uint32(0o700)
	privateFileMode      = uint32(0o600)
)

var (
	errSecureNotFound = errors.New("secure IPC entry not found")
	errSecureInvalid  = errors.New("secure IPC metadata is invalid")
)

type secureDirectory struct {
	fd       int
	ownerUID uint32
}

func splitSecurePath(path string) (string, string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", "", errSecureInvalid
	}
	directory := filepath.Dir(path)
	name := filepath.Base(path)
	if directory == path || name == "." || name == ".." || name == "" || filepath.Base(name) != name {
		return "", "", errSecureInvalid
	}
	return directory, name, nil
}

func openSecureDirectory(path string, ownerUID uint32) (*secureDirectory, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errSecureInvalid
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errSecureInvalid
	}
	directory := &secureDirectory{fd: fd, ownerUID: ownerUID}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR ||
		stat.Uid != ownerUID || stat.Mode&0o777 != privateDirectoryMode {
		directory.close()
		return nil, errSecureInvalid
	}
	return directory, nil
}

func (directory *secureDirectory) close() {
	if directory != nil && directory.fd >= 0 {
		_ = unix.Close(directory.fd)
		directory.fd = -1
	}
}

func (directory *secureDirectory) ensureAbsent(name string) error {
	if !validEntryName(name) {
		return errSecureInvalid
	}
	var stat unix.Stat_t
	err := unix.Fstatat(directory.fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		return ErrConflict
	}
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return errSecureInvalid
}

func (directory *secureDirectory) createExclusive(name string, contents []byte) error {
	if !validEntryName(name) || len(contents) == 0 {
		return errSecureInvalid
	}
	if err := directory.ensureAbsent(name); err != nil {
		return err
	}

	var randomBytes [16]byte
	if _, err := io.ReadFull(rand.Reader, randomBytes[:]); err != nil {
		return errSecureInvalid
	}
	temporaryName := "." + name + "." + hex.EncodeToString(randomBytes[:]) + ".tmp"
	clear(randomBytes[:])

	fd, err := unix.Openat(directory.fd, temporaryName,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, privateFileMode)
	if err != nil {
		return errSecureInvalid
	}
	temporaryExists := true
	defer func() {
		_ = unix.Close(fd)
		if temporaryExists {
			_ = unix.Unlinkat(directory.fd, temporaryName, 0)
		}
	}()

	if err := unix.Fchown(fd, int(directory.ownerUID), -1); err != nil {
		return errSecureInvalid
	}
	if err := unix.Fchmod(fd, privateFileMode); err != nil {
		return errSecureInvalid
	}
	if err := writeAll(fd, contents); err != nil {
		return errSecureInvalid
	}
	if err := unix.Fsync(fd); err != nil {
		return errSecureInvalid
	}
	var temporaryStat unix.Stat_t
	if err := unix.Fstat(fd, &temporaryStat); err != nil || !validFileStat(&temporaryStat, directory.ownerUID, int64(len(contents))) {
		return errSecureInvalid
	}
	if err := unix.Close(fd); err != nil {
		fd = -1
		return errSecureInvalid
	}
	fd = -1

	if err := unix.Linkat(directory.fd, temporaryName, directory.fd, name, 0); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return ErrConflict
		}
		return errSecureInvalid
	}
	if err := unix.Unlinkat(directory.fd, temporaryName, 0); err != nil {
		_ = unix.Unlinkat(directory.fd, name, 0)
		return errSecureInvalid
	}
	temporaryExists = false

	var installedStat unix.Stat_t
	if err := unix.Fstatat(directory.fd, name, &installedStat, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
		!validFileStat(&installedStat, directory.ownerUID, int64(len(contents))) {
		_ = unix.Unlinkat(directory.fd, name, 0)
		return errSecureInvalid
	}
	if err := unix.Fsync(directory.fd); err != nil {
		_ = unix.Unlinkat(directory.fd, name, 0)
		_ = unix.Fsync(directory.fd)
		return errSecureInvalid
	}
	return nil
}

func (directory *secureDirectory) readAndRemove(name string, maximumBytes int64) ([]byte, error) {
	if !validEntryName(name) || maximumBytes < 1 {
		return nil, errSecureInvalid
	}
	fd, err := unix.Openat(directory.fd, name, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, errSecureNotFound
	}
	if err != nil {
		return nil, errSecureInvalid
	}
	defer unix.Close(fd)

	stat, err := directory.validateOpenedEntry(fd, name, maximumBytes, false)
	if err != nil {
		return nil, err
	}
	contents := make([]byte, int(stat.Size))
	if err := readFull(fd, contents); err != nil {
		clear(contents)
		return nil, errSecureInvalid
	}
	var afterRead unix.Stat_t
	if err := unix.Fstat(fd, &afterRead); err != nil || !sameFile(&stat, &afterRead) || afterRead.Size != stat.Size {
		clear(contents)
		return nil, errSecureInvalid
	}
	if err := directory.wipeAndUnlink(fd, name, &stat); err != nil {
		clear(contents)
		return nil, err
	}
	return contents, nil
}

func (directory *secureDirectory) removeIfExists(name string, maximumBytes int64) error {
	if !validEntryName(name) || maximumBytes < 1 {
		return errSecureInvalid
	}
	fd, err := unix.Openat(directory.fd, name, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return errSecureInvalid
	}
	defer unix.Close(fd)
	stat, err := directory.validateOpenedEntry(fd, name, maximumBytes, true)
	if err != nil {
		return err
	}
	return directory.wipeAndUnlink(fd, name, &stat)
}

func (directory *secureDirectory) validateOpenedEntry(fd int, name string, maximumBytes int64, allowEmpty bool) (unix.Stat_t, error) {
	var openedStat unix.Stat_t
	if err := unix.Fstat(fd, &openedStat); err != nil || !validFileStat(&openedStat, directory.ownerUID, -1) {
		return unix.Stat_t{}, errSecureInvalid
	}
	if openedStat.Size > maximumBytes || (!allowEmpty && openedStat.Size < 1) || openedStat.Size < 0 {
		return unix.Stat_t{}, errSecureInvalid
	}
	var entryStat unix.Stat_t
	if err := unix.Fstatat(directory.fd, name, &entryStat, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
		!sameFile(&openedStat, &entryStat) || !validFileStat(&entryStat, directory.ownerUID, openedStat.Size) {
		return unix.Stat_t{}, errSecureInvalid
	}
	return openedStat, nil
}

func (directory *secureDirectory) wipeAndUnlink(fd int, name string, openedStat *unix.Stat_t) error {
	var zeros [4096]byte
	for offset := int64(0); offset < openedStat.Size; {
		length := int64(len(zeros))
		if remaining := openedStat.Size - offset; remaining < length {
			length = remaining
		}
		written, err := unix.Pwrite(fd, zeros[:length], offset)
		if err != nil || written != int(length) {
			return errSecureInvalid
		}
		offset += int64(written)
	}
	if err := unix.Fsync(fd); err != nil {
		return errSecureInvalid
	}
	var entryStat unix.Stat_t
	if err := unix.Fstatat(directory.fd, name, &entryStat, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
		!sameFile(openedStat, &entryStat) || !validFileStat(&entryStat, directory.ownerUID, openedStat.Size) {
		return errSecureInvalid
	}
	if err := unix.Unlinkat(directory.fd, name, 0); err != nil {
		return errSecureInvalid
	}
	if err := unix.Fsync(directory.fd); err != nil {
		return errSecureInvalid
	}
	return nil
}

func validEntryName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name
}

func validFileStat(stat *unix.Stat_t, ownerUID uint32, expectedSize int64) bool {
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o777 != privateFileMode || stat.Uid != ownerUID || stat.Nlink != 1 {
		return false
	}
	return expectedSize < 0 || stat.Size == expectedSize
}

func sameFile(left, right *unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino
}

func writeAll(fd int, contents []byte) error {
	for len(contents) > 0 {
		written, err := unix.Write(fd, contents)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || written < 1 {
			return errSecureInvalid
		}
		contents = contents[written:]
	}
	return nil
}

func readFull(fd int, destination []byte) error {
	offset := 0
	for offset < len(destination) {
		read, err := unix.Read(fd, destination[offset:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return errSecureInvalid
		}
		if read == 0 {
			return io.ErrUnexpectedEOF
		}
		offset += read
	}
	return nil
}
