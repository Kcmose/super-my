package setup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestRootFileStoreCreateReadReplaceRemove(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		t.Fatal(err)
	}
	uid, ok := fileOwnerUID(info)
	if !ok {
		t.Skip("platform does not expose file ownership")
	}
	store := newRootFileStoreForUID(uid)
	path := filepath.Join(directory, "state.json")
	if err := store.CreateAtomic(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAtomic(path, []byte("overwrite")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("exclusive create error = %v", err)
	}
	contents, err := store.Read(path, 16)
	if err != nil || string(contents) != "first" {
		t.Fatalf("read = %q, %v", contents, err)
	}
	fileInfo, _ := os.Lstat(path)
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", fileInfo.Mode().Perm())
	}
	if err := store.WriteAtomic(path, []byte("second")); err != nil {
		t.Fatal(err)
	}
	contents, _ = store.Read(path, 16)
	if string(contents) != "second" {
		t.Fatalf("contents = %q", contents)
	}
	if err := store.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(path, 16); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("missing read error = %v", err)
	}
}

func TestRootFileStoreRejectsSymlinksAndLoosePermissions(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Lstat(directory)
	uid, ok := fileOwnerUID(info)
	if !ok {
		t.Skip("platform does not expose file ownership")
	}
	store := newRootFileStoreForUID(uid)
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := store.Read(link, 16); err == nil {
		t.Fatal("symlink was accepted")
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(target, 16); err == nil {
		t.Fatal("loosely-permissioned file was accepted")
	}
	if err := os.Chmod(directory, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAtomic(filepath.Join(directory, "new"), []byte("value")); err == nil {
		t.Fatal("world-writable directory was accepted")
	}
}
