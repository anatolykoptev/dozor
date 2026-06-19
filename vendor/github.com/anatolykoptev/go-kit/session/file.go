package session

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	dirPerm  fs.FileMode = 0750
	filePerm fs.FileMode = 0600
)

// FileStore wraps InMemoryStore with JSON file persistence.
type FileStore struct {
	*InMemoryStore
	dir string
}

// NewFileStore creates a file-backed store, loading existing sessions.
func NewFileStore(dir string, opts Options) Store {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		panic("go-session: cannot create dir: " + err.Error())
	}

	fs := &FileStore{
		InMemoryStore: NewInMemoryStore(opts),
		dir:           dir,
	}
	fs.loadAll()
	return fs
}

func (f *FileStore) loadAll() {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return
	}

	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(f.dir, e.Name()))
		if err != nil {
			continue
		}

		var sess Session
		if err := json.Unmarshal(data, &sess); err != nil {
			continue // skip corrupt
		}

		f.mu.Lock()
		f.sessions[sess.Key] = &lockedSess{sess: &sess}
		f.mu.Unlock()
	}
}

func sanitizeKey(key string) string {
	r := strings.NewReplacer("/", "_", ":", "_", " ", "_")
	return r.Replace(key)
}

func (f *FileStore) filename(key string) string {
	return filepath.Join(f.dir, sanitizeKey(key)+".json")
}

// Save persists a session to disk atomically.
func (f *FileStore) Save(key string) error {
	f.mu.RLock()
	ls, ok := f.sessions[key]
	f.mu.RUnlock()
	if !ok {
		return nil
	}

	ls.mu.RLock()
	data, err := json.Marshal(ls.sess)
	ls.mu.RUnlock()
	if err != nil {
		return err
	}

	tmp := f.filename(key) + ".tmp"
	if err := os.WriteFile(tmp, data, filePerm); err != nil {
		return err
	}
	return os.Rename(tmp, f.filename(key))
}

// Delete removes from memory and disk.
func (f *FileStore) Delete(key string) error {
	if err := f.InMemoryStore.Delete(key); err != nil {
		return err
	}
	path := f.filename(key)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
