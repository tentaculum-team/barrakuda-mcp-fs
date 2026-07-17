package service

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// GrantStore holds the set of directories the user has granted the assistant
// full access to, persisted as JSON so grants survive restarts.
type GrantStore struct {
	mu   sync.Mutex
	path string
	dirs []string // absolute, cleaned directory paths
}

type grantsFile struct {
	Grants []string `json:"grants"`
}

// DefaultGrantsPath returns the grant file location under the user's config
// directory — deliberately outside the sandbox so the assistant cannot edit
// its own grants.
func DefaultGrantsPath() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "barrakuda", "fs-grants.json"), nil
}

// LoadGrantStore reads the grant file at path. A missing file yields an empty
// store; corrupt JSON is an error. Entries are only Clean'ed here (not
// symlink-resolved) so a temporarily missing granted dir cannot break boot.
func LoadGrantStore(path string) (*GrantStore, error) {
	g := &GrantStore{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return g, nil
	}
	if err != nil {
		return nil, err
	}
	var f grantsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	for _, d := range f.Grants {
		if filepath.IsAbs(d) {
			g.dirs = append(g.dirs, filepath.Clean(d))
		}
	}
	return g, nil
}

// Add records dir as granted and saves the file. Dirs already covered by an
// existing grant are deduped.
func (g *GrantStore) Add(dir string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	dir = filepath.Clean(dir)
	for _, d := range g.dirs {
		if pathsEqual(d, dir) || isWithin(d, dir) {
			return nil
		}
	}
	g.dirs = append(g.dirs, dir)
	return g.save()
}

// Contains reports whether p falls inside any granted directory. Nil-safe.
func (g *GrantStore) Contains(p string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, d := range g.dirs {
		if isWithin(d, p) {
			return true
		}
	}
	return false
}

// Dirs returns a copy of the granted directories. Nil-safe.
func (g *GrantStore) Dirs() []string {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, len(g.dirs))
	copy(out, g.dirs)
	return out
}

// Path returns the grant file location.
func (g *GrantStore) Path() string {
	if g == nil {
		return ""
	}
	return g.path
}

func (g *GrantStore) save() error {
	data, err := json.MarshalIndent(grantsFile{Grants: g.dirs}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(g.path), 0o700); err != nil {
		return err
	}
	// ponytail: plain WriteFile, no atomic rename — single writer, tiny file
	return os.WriteFile(g.path, data, 0o600)
}
