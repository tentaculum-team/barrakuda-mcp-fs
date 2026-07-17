package repository

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"unicode/utf8"

	"barrakuda-mcp-fs/internal/domain"
	errorsx "barrakuda-mcp-fs/pkg/errors"
)

type FileRepository struct{}

func NewFileRepository() *FileRepository {
	return &FileRepository{}
}

func (r *FileRepository) List(absDir string) ([]domain.FileEntry, error) {
	info, err := os.Stat(absDir)
	if err != nil {
		return nil, mapStatErr(err)
	}
	if !info.IsDir() {
		return nil, errorsx.ErrNotADirectory
	}

	dirEntries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, err
	}

	entries := make([]domain.FileEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		fi, err := de.Info()
		if err != nil {
			continue
		}
		entries = append(entries, domain.FileEntry{
			Name:    fi.Name(),
			IsDir:   fi.IsDir(),
			Size:    fi.Size(),
			ModTime: fi.ModTime(),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Name < entries[j].Name
	})

	return entries, nil
}

func (r *FileRepository) Read(absPath string, maxBytes int64) (domain.ReadResult, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return domain.ReadResult{}, mapStatErr(err)
	}
	if info.IsDir() {
		return domain.ReadResult{}, errorsx.ErrNotAFile
	}
	if !info.Mode().IsRegular() {
		return domain.ReadResult{}, errorsx.ErrNotAFile
	}

	f, err := os.Open(absPath)
	if err != nil {
		return domain.ReadResult{}, mapStatErr(err)
	}
	defer f.Close()

	size := info.Size()
	truncated := false

	limit := maxBytes
	buf, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return domain.ReadResult{}, err
	}
	if int64(len(buf)) > limit {
		buf = buf[:limit]
		truncated = true
	}

	if truncated {
		buf = trimIncompleteTrailingRune(buf)
	}

	if !isProbablyText(buf) {
		return domain.ReadResult{}, errorsx.ErrNotText
	}

	return domain.ReadResult{
		Content:   string(buf),
		Truncated: truncated,
		SizeBytes: size,
	}, nil
}

func (r *FileRepository) Write(absPath, content string, maxBytes int64, createDirs bool) (domain.WriteResult, error) {
	if int64(len(content)) > maxBytes {
		return domain.WriteResult{}, errorsx.ErrFileTooLarge
	}

	if info, err := os.Stat(absPath); err == nil && info.IsDir() {
		return domain.WriteResult{}, errorsx.ErrNotAFile
	}

	created := false
	if _, err := os.Stat(absPath); errors.Is(err, os.ErrNotExist) {
		created = true
	}

	if createDirs {
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			return domain.WriteResult{}, err
		}
	}

	if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
		return domain.WriteResult{}, err
	}

	return domain.WriteResult{BytesWritten: len(content), Created: created}, nil
}

func (r *FileRepository) Delete(absPath string) error {
	if _, err := os.Stat(absPath); err != nil {
		return mapStatErr(err)
	}
	if err := os.Remove(absPath); err != nil {
		return err
	}
	return nil
}

func mapStatErr(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return errorsx.ErrNotFound
	}
	return err
}

func isProbablyText(buf []byte) bool {
	for _, b := range buf {
		if b == 0x00 {
			return false
		}
	}
	return utf8.Valid(buf)
}

func trimIncompleteTrailingRune(buf []byte) []byte {
	for i := 0; i < utf8.UTFMax-1 && len(buf) > 0; i++ {
		r, size := utf8.DecodeLastRune(buf)
		if r != utf8.RuneError || size != 1 {
			return buf
		}
		buf = buf[:len(buf)-1]
	}
	return buf
}
