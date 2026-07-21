package service

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"barrakuda-mcp-fs/internal/repository"
	errorsx "barrakuda-mcp-fs/pkg/errors"
)

func newSvc(t *testing.T) (*FileService, string) {
	t.Helper()
	root := t.TempDir()
	svc, err := NewFileServiceWithRoot(repository.NewFileRepository(), root)
	if err != nil {
		t.Fatalf("NewFileServiceWithRoot: %v", err)
	}
	return svc, svc.Root()
}

func TestResolveBlocksParentTraversal(t *testing.T) {
	svc, _ := newSvc(t)
	for _, p := range []string{
		"../../../etc/passwd",
		"..",
		"../sibling",
		"foo/../../bar",
		"a/b/c/../../../../../secret",
	} {
		if _, err := svc.resolvePath(p); !errors.Is(err, errorsx.ErrPathOutsideSandbox) {
			t.Errorf("resolvePath(%q) err = %v, want ErrPathOutsideSandbox", p, err)
		}
	}
}

func TestResolveBlocksAbsolutePathOutsideAllowed(t *testing.T) {
	svc, _ := newSvc(t)

	var absOutside string
	if runtime.GOOS == "windows" {
		absOutside = `C:\Windows\System32\drivers\etc\hosts`
	} else {
		absOutside = "/etc/passwd"
	}

	if _, err := svc.resolvePath(absOutside); !errors.Is(err, errorsx.ErrPathOutsideSandbox) {
		t.Fatalf("resolvePath(%q) err = %v, want ErrPathOutsideSandbox", absOutside, err)
	}

	// Absolute path inside the sandbox root is now valid.
	if _, err := svc.resolvePath(svc.Root()); err != nil {
		t.Fatalf("resolvePath(root as abs) err = %v, want success", err)
	}
}

func TestResolveEmptyPathRejected(t *testing.T) {
	svc, _ := newSvc(t)
	for _, p := range []string{"", "   "} {
		if _, err := svc.resolvePath(p); !errors.Is(err, errorsx.ErrEmptyPath) {
			t.Errorf("resolvePath(%q) err = %v, want ErrEmptyPath", p, err)
		}
	}
}

func TestIsWithinRejectsSiblingPrefix(t *testing.T) {
	var root, sibling, inside string
	if runtime.GOOS == "windows" {
		root, sibling, inside = `C:\work`, `C:\work-evil\secret`, `C:\work\ok.txt`
	} else {
		root, sibling, inside = "/work", "/work-evil/secret", "/work/ok.txt"
	}
	if isWithin(root, sibling) {
		t.Errorf("isWithin(%q, %q) = true; sibling with matching string prefix must be rejected", root, sibling)
	}
	if !isWithin(root, inside) {
		t.Errorf("isWithin(%q, %q) = false; a genuine child must be accepted", root, inside)
	}
	if !isWithin(root, root) {
		t.Errorf("isWithin(%q, itself) = false; root must contain itself", root)
	}
}

func TestResolveBlocksSymlinkEscape(t *testing.T) {
	svc, root := newSvc(t)

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatalf("seeding outside file: %v", err)
	}

	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlink on this platform/permission set (%v) — symlink-escape case not verified here", err)
	}

	if _, err := svc.resolvePath("escape"); !errors.Is(err, errorsx.ErrPathOutsideSandbox) {
		t.Fatalf("resolvePath(escape symlink) err = %v, want ErrPathOutsideSandbox", err)
	}

	if _, err := svc.resolvePath("escape/secret.txt"); !errors.Is(err, errorsx.ErrPathOutsideSandbox) {
		t.Fatalf("resolvePath(escape/secret.txt) err = %v, want ErrPathOutsideSandbox", err)
	}

	if _, err := svc.Read("escape/secret.txt", 0); !errors.Is(err, errorsx.ErrPathOutsideSandbox) {
		t.Fatalf("Read via symlink err = %v, want ErrPathOutsideSandbox", err)
	}
}

func TestResolveBlocksSymlinkEscapeOnNewFile(t *testing.T) {
	svc, root := newSvc(t)
	outside := t.TempDir()

	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlink on this platform/permission set (%v) — case not verified here", err)
	}

	if _, err := svc.resolvePath("escape/newfile.txt"); !errors.Is(err, errorsx.ErrPathOutsideSandbox) {
		t.Fatalf("resolvePath(new file under escape) err = %v, want ErrPathOutsideSandbox", err)
	}

	if _, err := svc.Write("escape/newfile.txt", "x", true); !errors.Is(err, errorsx.ErrPathOutsideSandbox) {
		t.Fatalf("Write under escape symlink err = %v, want ErrPathOutsideSandbox", err)
	}

	if _, statErr := os.Stat(filepath.Join(outside, "newfile.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("SECURITY: file was written outside the sandbox through a symlink (stat err: %v)", statErr)
	}
}

func TestResolveAllowsInternalSymlink(t *testing.T) {
	svc, root := newSvc(t)

	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "f.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("cannot create symlink (%v) — internal-symlink case not verified", err)
	}

	got, err := svc.resolvePath("link/f.txt")
	if err != nil {
		t.Fatalf("resolvePath(internal symlink) err = %v, want success", err)
	}
	if !strings.HasPrefix(got, svc.Root()) {
		t.Fatalf("resolved %q not under root %q", got, svc.Root())
	}
}

func TestHappyPathWriteReadListDelete(t *testing.T) {
	svc, _ := newSvc(t)

	if _, err := svc.Write("dir/hello.txt", "hello world", true); err != nil {
		t.Fatalf("Write: %v", err)
	}

	res, err := svc.Read("dir/hello.txt", 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.Content != "hello world" {
		t.Fatalf("Read content = %q, want %q", res.Content, "hello world")
	}
	if res.Truncated {
		t.Fatal("Read Truncated = true, want false")
	}

	entries, err := svc.List("dir")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "hello.txt" {
		t.Fatalf("List = %+v, want single hello.txt", entries)
	}

	if err := svc.Delete("dir/hello.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Read("dir/hello.txt", 0); !errors.Is(err, errorsx.ErrNotFound) {
		t.Fatalf("Read after delete err = %v, want ErrNotFound", err)
	}
}

func TestWriteRejectsCreateDirsFalseMissingParent(t *testing.T) {
	svc, root := newSvc(t)
	if _, err := svc.Write("nope/deep/x.txt", "x", false); err == nil {
		t.Fatal("Write with create_dirs=false and missing parent: err = nil, want failure")
	}
	if _, statErr := os.Stat(filepath.Join(root, "nope")); !os.IsNotExist(statErr) {
		t.Fatalf("parent dir was created despite create_dirs=false (stat err: %v)", statErr)
	}
}

func TestDeleteRootBlocked(t *testing.T) {
	svc, root := newSvc(t)
	if err := svc.Delete("."); !errors.Is(err, errorsx.ErrCannotDeleteRoot) {
		t.Fatalf("Delete(\".\") err = %v, want ErrCannotDeleteRoot", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("SECURITY: sandbox root no longer exists after Delete(\".\"): %v", err)
	}
}

// newSvcWithGrant returns a service whose grant store contains one granted dir.
func newSvcWithGrant(t *testing.T) (*FileService, string) {
	t.Helper()
	svc, _ := newSvc(t)
	granted, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g, err := LoadGrantStore(filepath.Join(t.TempDir(), "fs-grants.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Add(granted); err != nil {
		t.Fatal(err)
	}
	svc.SetGrantStore(g)
	return svc, granted
}

func TestGrantedAbsolutePathAllowsOps(t *testing.T) {
	svc, granted := newSvcWithGrant(t)

	file := filepath.Join(granted, "sub", "hello.txt")
	if _, err := svc.Write(file, "hi from grant", true); err != nil {
		t.Fatalf("Write in granted dir: %v", err)
	}
	res, err := svc.Read(file, 0)
	if err != nil {
		t.Fatalf("Read in granted dir: %v", err)
	}
	if res.Content != "hi from grant" {
		t.Fatalf("Read content = %q", res.Content)
	}
	entries, err := svc.List(filepath.Join(granted, "sub"))
	if err != nil {
		t.Fatalf("List in granted dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "hello.txt" {
		t.Fatalf("List = %+v, want single hello.txt", entries)
	}
	if err := svc.Delete(file); err != nil {
		t.Fatalf("Delete in granted dir: %v", err)
	}
}

func TestUngrantedAbsolutePathStillBlocked(t *testing.T) {
	svc, _ := newSvcWithGrant(t)
	other := t.TempDir() // not granted
	if _, err := svc.resolvePath(filepath.Join(other, "f.txt")); !errors.Is(err, errorsx.ErrPathOutsideSandbox) {
		t.Fatalf("resolvePath(ungranted abs) err = %v, want ErrPathOutsideSandbox", err)
	}
}

func TestRelativeTraversalCannotReachGrant(t *testing.T) {
	svc, granted := newSvcWithGrant(t)
	rel, err := filepath.Rel(svc.Root(), granted)
	if err != nil || !strings.Contains(rel, "..") {
		t.Skipf("cannot build .. path from root to granted dir (rel=%q err=%v)", rel, err)
	}
	if _, err := svc.resolvePath(rel); !errors.Is(err, errorsx.ErrPathOutsideSandbox) {
		t.Fatalf("resolvePath(relative .. into grant) err = %v, want ErrPathOutsideSandbox", err)
	}
}

func TestGrantedSymlinkEscapeBlocked(t *testing.T) {
	svc, granted := newSvcWithGrant(t)
	outside := t.TempDir()

	link := filepath.Join(granted, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlink (%v) — case not verified", err)
	}
	if _, err := svc.resolvePath(filepath.Join(link, "x.txt")); !errors.Is(err, errorsx.ErrPathOutsideSandbox) {
		t.Fatalf("resolvePath(symlink escaping grant) err = %v, want ErrPathOutsideSandbox", err)
	}
}

func TestEditReplacesUniqueMatch(t *testing.T) {
	svc, _ := newSvc(t)
	if _, err := svc.Write("f.txt", "hello world\ngoodbye world\n", true); err != nil {
		t.Fatalf("Write: %v", err)
	}

	res, err := svc.Edit("f.txt", "hello world", "hi world", false)
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if res.Replacements != 1 {
		t.Fatalf("Replacements = %d, want 1", res.Replacements)
	}

	got, err := svc.Read("f.txt", 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := "hi world\ngoodbye world\n"
	if got.Content != want {
		t.Fatalf("content = %q, want %q", got.Content, want)
	}
}

func TestEditNoMatch(t *testing.T) {
	svc, _ := newSvc(t)
	if _, err := svc.Write("f.txt", "hello world\n", true); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := svc.Edit("f.txt", "not there", "x", false); !errors.Is(err, errorsx.ErrNoMatch) {
		t.Fatalf("Edit err = %v, want ErrNoMatch", err)
	}
}

func TestEditAmbiguousMatchWithoutReplaceAll(t *testing.T) {
	svc, _ := newSvc(t)
	if _, err := svc.Write("f.txt", "x\nx\nx\n", true); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := svc.Edit("f.txt", "x", "y", false); !errors.Is(err, errorsx.ErrAmbiguousMatch) {
		t.Fatalf("Edit err = %v, want ErrAmbiguousMatch", err)
	}
	// arquivo não deve ter sido tocado
	got, err := svc.Read("f.txt", 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Content != "x\nx\nx\n" {
		t.Fatalf("content changed after ambiguous edit: %q", got.Content)
	}
}

func TestEditReplaceAllReplacesEveryOccurrence(t *testing.T) {
	svc, _ := newSvc(t)
	if _, err := svc.Write("f.txt", "x\nx\nx\n", true); err != nil {
		t.Fatalf("Write: %v", err)
	}
	res, err := svc.Edit("f.txt", "x", "y", true)
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if res.Replacements != 3 {
		t.Fatalf("Replacements = %d, want 3", res.Replacements)
	}
	got, err := svc.Read("f.txt", 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Content != "y\ny\ny\n" {
		t.Fatalf("content = %q, want %q", got.Content, "y\ny\ny\n")
	}
}

func TestEditOutsideSandboxBlocked(t *testing.T) {
	svc, _ := newSvc(t)
	outside := filepath.Join(t.TempDir(), "f.txt")
	if _, err := svc.Edit(outside, "a", "b", false); !errors.Is(err, errorsx.ErrPathOutsideSandbox) {
		t.Fatalf("Edit(outside) err = %v, want ErrPathOutsideSandbox", err)
	}
}

func TestSearchFindsMatchInNestedDir(t *testing.T) {
	svc, _ := newSvc(t)
	if _, err := svc.Write("a/b/needle.go", "package x\n\nfunc Needle() {}\n", true); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := svc.Write("a/other.go", "package x\n", true); err != nil {
		t.Fatalf("Write: %v", err)
	}

	res, err := svc.Search(".", "Needle", true, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Matches) != 1 {
		t.Fatalf("matches = %+v, want exactly 1", res.Matches)
	}
	m := res.Matches[0]
	if m.Line != 3 {
		t.Fatalf("match line = %d, want 3", m.Line)
	}
	wantPath := filepath.Join("a", "b", "needle.go")
	if m.Path != wantPath {
		t.Fatalf("match path = %q, want %q", m.Path, wantPath)
	}
}

func TestSearchInvalidPatternRejectedBeforeWalk(t *testing.T) {
	svc, _ := newSvc(t)
	if _, err := svc.Write("f.txt", "hello\n", true); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := svc.Search(".", "(unclosed", true, 0); !errors.Is(err, errorsx.ErrInvalidPattern) {
		t.Fatalf("Search err = %v, want ErrInvalidPattern", err)
	}
}

func TestSearchSkipsBinaryFile(t *testing.T) {
	svc, root := newSvc(t)
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0x00, 0x01, 'x'}, 0o644); err != nil {
		t.Fatalf("seed binary file: %v", err)
	}
	if _, err := svc.Write("text.txt", "x marks the spot\n", true); err != nil {
		t.Fatalf("Write: %v", err)
	}

	res, err := svc.Search(".", "x", true, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Matches) != 1 || res.Matches[0].Path != "text.txt" {
		t.Fatalf("matches = %+v, want only text.txt matched (binary file skipped)", res.Matches)
	}
}

func TestSearchMaxResultsTruncates(t *testing.T) {
	svc, _ := newSvc(t)
	if _, err := svc.Write("f.txt", "x\nx\nx\nx\nx\n", true); err != nil {
		t.Fatalf("Write: %v", err)
	}

	res, err := svc.Search(".", "x", true, 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Matches) != 2 {
		t.Fatalf("matches = %d, want 2", len(res.Matches))
	}
	if !res.Truncated {
		t.Fatal("Truncated = false, want true")
	}
}

func TestSearchOutsideSandboxBlocked(t *testing.T) {
	svc, _ := newSvc(t)
	outside := t.TempDir()
	if _, err := svc.Search(outside, "x", true, 0); !errors.Is(err, errorsx.ErrPathOutsideSandbox) {
		t.Fatalf("Search(outside) err = %v, want ErrPathOutsideSandbox", err)
	}
}

func TestResolveAccessTargetAlreadyAllowed(t *testing.T) {
	svc, root := newSvc(t)
	if dir, err := svc.ResolveAccessTarget(root); err != nil || dir != "" {
		t.Fatalf("ResolveAccessTarget(root) = (%q, %v), want (\"\", nil)", dir, err)
	}

	svcG, granted := newSvcWithGrant(t)
	if dir, err := svcG.ResolveAccessTarget(filepath.Join(granted, "sub", "f.txt")); err != nil || dir != "" {
		t.Fatalf("ResolveAccessTarget(granted subpath) = (%q, %v), want (\"\", nil)", dir, err)
	}
}

func TestResolveAccessTargetExistingDirOutsideSandbox(t *testing.T) {
	svc, _ := newSvc(t)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, err := svc.ResolveAccessTarget(outside)
	if err != nil {
		t.Fatalf("ResolveAccessTarget: %v", err)
	}
	if dir != outside {
		t.Fatalf("ResolveAccessTarget(existing outside dir) = %q, want %q", dir, outside)
	}
}

func TestResolveAccessTargetNewFileUsesParentDir(t *testing.T) {
	svc, _ := newSvc(t)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	notYetCreated := filepath.Join(outside, "hello.py")
	dir, err := svc.ResolveAccessTarget(notYetCreated)
	if err != nil {
		t.Fatalf("ResolveAccessTarget: %v", err)
	}
	if dir != outside {
		t.Fatalf("ResolveAccessTarget(new file) = %q, want parent %q", dir, outside)
	}
}

func TestResolveAccessTargetDeepMissingPathNotFound(t *testing.T) {
	svc, _ := newSvc(t)
	if _, err := svc.ResolveAccessTarget(filepath.Join(t.TempDir(), "does", "not", "exist", "f.txt")); err == nil {
		t.Fatal("ResolveAccessTarget(deep missing path): err = nil, want error")
	}
}

func TestDeleteGrantedRootBlocked(t *testing.T) {
	svc, granted := newSvcWithGrant(t)
	if err := svc.Delete(granted); !errors.Is(err, errorsx.ErrCannotDeleteRoot) {
		t.Fatalf("Delete(granted root) err = %v, want ErrCannotDeleteRoot", err)
	}
	if _, err := os.Stat(granted); err != nil {
		t.Fatalf("SECURITY: granted root no longer exists after Delete: %v", err)
	}
}
