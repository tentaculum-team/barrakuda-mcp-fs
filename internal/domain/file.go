package domain

import "time"

type FileEntry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time
}

type ReadResult struct {
	Content   string
	Truncated bool
	SizeBytes int64
}

type WriteResult struct {
	BytesWritten int
	Created      bool
}

type EditResult struct {
	BytesWritten int
	Replacements int
}

// SearchMatch is one matching line found by FileService.Search. Path is
// rendered the way callers should reference it again (relative to the
// sandbox root, or absolute if only reachable via a grant) — same
// convention fs.list/fs.read already expect as input.
type SearchMatch struct {
	Path string
	Line int
	Text string
}

type SearchResult struct {
	Matches   []SearchMatch
	Truncated bool
}

// GlobResult is the outcome of FileService.Glob — file NAMES matching a
// pattern, unlike Search which matches file CONTENT. Paths are rendered the
// same way SearchMatch.Path is (relative to the sandbox root, or absolute
// if only reachable via a grant).
type GlobResult struct {
	Paths     []string
	Truncated bool
}
