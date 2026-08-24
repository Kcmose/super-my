package setup

import (
	"errors"
	"io"
	"io/fs"
	"sync"
	"time"
)

type memoryFiles struct {
	mu    sync.Mutex
	files map[string][]byte
}

func newMemoryFiles() *memoryFiles {
	return &memoryFiles{files: make(map[string][]byte)}
}

func (files *memoryFiles) Read(path string, _ int64) ([]byte, error) {
	files.mu.Lock()
	defer files.mu.Unlock()
	contents, ok := files.files[path]
	if !ok {
		return nil, ErrFileNotFound
	}
	return append([]byte(nil), contents...), nil
}

func (files *memoryFiles) CreateAtomic(path string, contents []byte) error {
	files.mu.Lock()
	defer files.mu.Unlock()
	if _, exists := files.files[path]; exists {
		return fs.ErrExist
	}
	files.files[path] = append([]byte(nil), contents...)
	return nil
}

func (files *memoryFiles) WriteAtomic(path string, contents []byte) error {
	files.mu.Lock()
	defer files.mu.Unlock()
	if _, exists := files.files[path]; !exists {
		return errors.New("missing file")
	}
	files.files[path] = append([]byte(nil), contents...)
	return nil
}

func (files *memoryFiles) Remove(path string) error {
	files.mu.Lock()
	defer files.mu.Unlock()
	if _, exists := files.files[path]; !exists {
		return ErrFileNotFound
	}
	delete(files.files, path)
	return nil
}

func newTestManager(files *memoryFiles, now func() time.Time, random io.Reader) *Manager {
	states, err := NewStateStore(files, "/state.json")
	if err != nil {
		panic(err)
	}
	manager, err := NewManager(states, WithClock(now), WithRandom(random))
	if err != nil {
		panic(err)
	}
	return manager
}
