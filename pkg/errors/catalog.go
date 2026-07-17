package errorsx

import "errors"

var (
	ErrPathOutsideSandbox = errors.New("path resolves outside the sandbox root")
	ErrEmptyPath          = errors.New("path must not be empty")
	ErrFileTooLarge       = errors.New("file exceeds the configured size limit")
	ErrNotFound           = errors.New("path not found")
	ErrNotAFile           = errors.New("path is not a regular file")
	ErrNotADirectory      = errors.New("path is not a directory")
	ErrNotText            = errors.New("file is not valid UTF-8 text")
	ErrCannotDeleteRoot   = errors.New("refusing to delete the sandbox root")
	ErrNoMatch            = errors.New("old_string not found in the file")
	ErrAmbiguousMatch     = errors.New("old_string matches more than once in the file")
	ErrInvalidPattern     = errors.New("invalid search pattern")
)
