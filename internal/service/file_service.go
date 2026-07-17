package service

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"barrakuda-mcp-fs/internal/domain"
	"barrakuda-mcp-fs/internal/repository"
	errorsx "barrakuda-mcp-fs/pkg/errors"
)

const (
	DefaultMaxReadBytes  = 10 * 1024 * 1024 // 10MB
	MaxReadBytes         = 50 * 1024 * 1024 // 50MB
	DefaultMaxWriteBytes = 10 * 1024 * 1024 // 10MB

	DefaultMaxSearchResults = 200
	MaxSearchResults        = 1000

	searchLineTruncateAt = 200 // runes per matched line shown in results
)

type FileService struct {
	root          string
	grants        *GrantStore // nil = no grants; user-approved extra roots
	repo          *repository.FileRepository
	maxReadBytes  int64
	maxWriteBytes int64
}

func NewFileService(repo *repository.FileRepository) (*FileService, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(wd)
	if err != nil {
		return nil, err
	}
	root = filepath.Clean(root)
	return &FileService{
		root:          root,
		repo:          repo,
		maxReadBytes:  DefaultMaxReadBytes,
		maxWriteBytes: DefaultMaxWriteBytes,
	}, nil
}

func NewFileServiceWithRoot(repo *repository.FileRepository, root string) (*FileService, error) {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	return &FileService{
		root:          filepath.Clean(real),
		repo:          repo,
		maxReadBytes:  DefaultMaxReadBytes,
		maxWriteBytes: DefaultMaxWriteBytes,
	}, nil
}

func (s *FileService) Root() string {
	return s.root
}

func (s *FileService) SetGrantStore(g *GrantStore) {
	s.grants = g
}

// WithinAllowed reports whether the cleaned absolute path p falls inside the
// sandbox root or any user-granted directory.
func (s *FileService) WithinAllowed(p string) bool {
	return isWithin(s.root, p) || s.grants.Contains(p)
}

// allowedRoots returns the sandbox root plus all granted directories.
func (s *FileService) allowedRoots() []string {
	return append([]string{s.root}, s.grants.Dirs()...)
}

func (s *FileService) resolvePath(relPath string) (string, error) {
	if strings.TrimSpace(relPath) == "" {
		return "", errorsx.ErrEmptyPath
	}

	if filepath.IsAbs(relPath) {
		// Absolute paths are valid inside any allowed root (sandbox or
		// user-granted folder). The boundary check happens INSIDE
		// resolveSymlinks, after EvalSymlinks normalization — a Windows 8.3
		// short path (VIITOJ~1) or a symlinked prefix only matches the
		// (normalized) allowed roots once resolved to its real form.
		return s.resolveSymlinks(filepath.Clean(relPath))
	}

	// Relative paths keep sandbox-only semantics: they may never .. their
	// way into a granted folder — those are addressed by absolute path only.
	resolved := filepath.Join(s.root, relPath)
	if !isWithin(s.root, resolved) {
		return "", errorsx.ErrPathOutsideSandbox
	}

	return s.resolveSymlinks(resolved)
}

func (s *FileService) resolveSymlinks(candidate string) (string, error) {
	existing := candidate
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", errorsx.ErrPathOutsideSandbox
		}
		existing = parent
	}

	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	if !s.WithinAllowed(real) {
		return "", errorsx.ErrPathOutsideSandbox
	}

	if existing == candidate {
		return real, nil
	}

	suffix, err := filepath.Rel(existing, candidate)
	if err != nil {
		return "", errorsx.ErrPathOutsideSandbox
	}

	final := filepath.Join(real, suffix)
	if !s.WithinAllowed(final) {
		return "", errorsx.ErrPathOutsideSandbox
	}

	return final, nil
}

func (s *FileService) List(relDir string) ([]domain.FileEntry, error) {
	abs, err := s.resolvePath(relDir)
	if err != nil {
		return nil, err
	}
	return s.repo.List(abs)
}

func (s *FileService) Read(relPath string, maxBytes int64) (domain.ReadResult, error) {
	abs, err := s.resolvePath(relPath)
	if err != nil {
		return domain.ReadResult{}, err
	}
	limit := s.maxReadBytes
	if maxBytes > 0 && maxBytes < s.maxReadBytes {
		limit = maxBytes
	}
	return s.repo.Read(abs, limit)
}

func (s *FileService) Write(relPath, content string, createDirs bool) (domain.WriteResult, error) {
	abs, err := s.resolvePath(relPath)
	if err != nil {
		return domain.WriteResult{}, err
	}
	return s.repo.Write(abs, content, s.maxWriteBytes, createDirs)
}

func (s *FileService) Delete(relPath string) error {
	abs, err := s.resolvePath(relPath)
	if err != nil {
		return err
	}
	for _, root := range s.allowedRoots() {
		if pathsEqual(abs, root) {
			return errorsx.ErrCannotDeleteRoot
		}
	}
	return s.repo.Delete(abs)
}

// Edit replaces oldString with newString inside the file at relPath. Reuses
// repo.Read (binary/size/truncation handling) and repo.Write (size cap) —
// this is pure find/replace, no diff/patch format, no line counting.
func (s *FileService) Edit(relPath, oldString, newString string, replaceAll bool) (domain.EditResult, error) {
	abs, err := s.resolvePath(relPath)
	if err != nil {
		return domain.EditResult{}, err
	}

	read, err := s.repo.Read(abs, s.maxWriteBytes)
	if err != nil {
		return domain.EditResult{}, err
	}
	if read.Truncated {
		// A truncated view isn't a safe basis for a full-file rewrite — the
		// edit could silently drop the untruncated tail of the file.
		return domain.EditResult{}, errorsx.ErrFileTooLarge
	}

	count := strings.Count(read.Content, oldString)
	if count == 0 {
		return domain.EditResult{}, errorsx.ErrNoMatch
	}
	if count > 1 && !replaceAll {
		return domain.EditResult{}, errorsx.ErrAmbiguousMatch
	}

	limit := 1
	replacements := 1
	if replaceAll {
		limit = -1
		replacements = count
	}
	newContent := strings.Replace(read.Content, oldString, newString, limit)

	res, err := s.repo.Write(abs, newContent, s.maxWriteBytes, false)
	if err != nil {
		return domain.EditResult{}, err
	}
	return domain.EditResult{BytesWritten: res.BytesWritten, Replacements: replacements}, nil
}

// Search greps recursively for pattern (a regexp) starting at relPath
// (default the sandbox root), respecting the same sandbox/grant boundary as
// every other tool. Binary and oversized files are skipped, not errored —
// only an unreadable start path or an invalid pattern is a hard error.
func (s *FileService) Search(relPath, pattern string, caseSensitive bool, maxResults int) (domain.SearchResult, error) {
	abs, err := s.resolvePath(relPath)
	if err != nil {
		return domain.SearchResult{}, err
	}
	if _, err := os.Stat(abs); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.SearchResult{}, errorsx.ErrNotFound
		}
		return domain.SearchResult{}, err
	}

	expr := pattern
	if !caseSensitive {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return domain.SearchResult{}, fmt.Errorf("%w: %s", errorsx.ErrInvalidPattern, err)
	}

	limit := DefaultMaxSearchResults
	if maxResults > 0 {
		limit = maxResults
	}
	if limit > MaxSearchResults {
		limit = MaxSearchResults
	}

	var matches []domain.SearchMatch
	truncated := false

	walkErr := filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			// Unreadable entry (permissions, etc.) or a directory itself:
			// skip and keep walking — one bad entry shouldn't abort the
			// whole search.
			return nil
		}

		// Directory symlinks aren't followed by WalkDir, but a FILE entry
		// that is itself a symlink pointing outside the sandbox/grants
		// would otherwise be read straight through (os.Open follows
		// symlinks) — close that gap the same way resolveSymlinks does for
		// direct fs.* calls.
		real, err := filepath.EvalSymlinks(p)
		if err != nil || !s.WithinAllowed(real) {
			return nil
		}

		read, err := s.repo.Read(p, s.maxReadBytes)
		if err != nil {
			return nil // binary/unreadable — not a search error, just unsearchable
		}

		for i, line := range strings.Split(read.Content, "\n") {
			if !re.MatchString(line) {
				continue
			}
			matches = append(matches, domain.SearchMatch{
				Path: s.displayPath(p),
				Line: i + 1,
				Text: truncateRunes(line, searchLineTruncateAt),
			})
			if len(matches) >= limit {
				truncated = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil {
		return domain.SearchResult{}, walkErr
	}

	return domain.SearchResult{Matches: matches, Truncated: truncated}, nil
}

// displayPath renders an absolute path the way callers should reference it
// again: relative to the sandbox root when inside it, absolute otherwise
// (a grant, reachable only via absolute path — same convention every fs.*
// tool already expects as input).
func (s *FileService) displayPath(abs string) string {
	if isWithin(s.root, abs) || pathsEqual(abs, s.root) {
		if rel, err := filepath.Rel(s.root, abs); err == nil {
			return rel
		}
	}
	return abs
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func isWithin(root, p string) bool {
	if pathsEqual(p, root) {
		return true
	}
	prefix := root + string(os.PathSeparator)
	if caseInsensitiveFS() {
		return strings.HasPrefix(strings.ToLower(p), strings.ToLower(prefix))
	}
	return strings.HasPrefix(p, prefix)
}

func pathsEqual(a, b string) bool {
	if caseInsensitiveFS() {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func caseInsensitiveFS() bool {
	return runtime.GOOS == "windows"
}
