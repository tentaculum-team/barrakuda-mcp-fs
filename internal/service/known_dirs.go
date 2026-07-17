package service

import (
	"os"
	"path/filepath"
)

// KnownDir is a well-known user folder (paths only, contents never read).
type KnownDir struct {
	Name string
	Path string
}

// KnownDirs returns the well-known folders of the local user account that
// actually exist on this machine: home plus Desktop/Documents/Downloads,
// including OneDrive-redirected variants on Windows. The model uses these
// paths with fs.request_access — the folders are NOT accessible until the
// user grants them.
func KnownDirs() []KnownDir {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	out := []KnownDir{{Name: "home", Path: home}}
	bases := []string{home}
	// ponytail: OneDrive env var covers the common redirect; full known-folder
	// resolution via Windows registry/shell API if someone's setup needs it.
	if od := os.Getenv("OneDrive"); od != "" && !pathsEqual(od, home) {
		bases = append(bases, od)
	}
	for _, base := range bases {
		for _, name := range []string{"Desktop", "Documents", "Downloads"} {
			p := filepath.Join(base, name)
			if info, err := os.Stat(p); err == nil && info.IsDir() {
				out = append(out, KnownDir{Name: name, Path: p})
			}
		}
	}
	return out
}
