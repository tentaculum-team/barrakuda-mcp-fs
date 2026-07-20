package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"barrakuda-mcp-fs/internal/domain"
	"barrakuda-mcp-fs/internal/service"
	errorsx "barrakuda-mcp-fs/pkg/errors"
)

func NewServer(fileService *service.FileService, grants *service.GrantStore) *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer(
		"barrakuda-mcp-fs",
		"0.1.0",
		mcpserver.WithToolCapabilities(false),
		mcpserver.WithElicitation(),
	)

	s.AddTool(fsListTool(fileService), fsListHandler(fileService))
	s.AddTool(fsReadTool(fileService), fsReadHandler(fileService))
	s.AddTool(fsEditTool(fileService), fsEditHandler(fileService))
	s.AddTool(fsSearchTool(fileService), fsSearchHandler(fileService))
	s.AddTool(fsWriteTool(fileService), fsWriteHandler(fileService))
	s.AddTool(fsDeleteTool(fileService), fsDeleteHandler(fileService))
	s.AddTool(fsRequestAccessTool(grants), fsRequestAccessHandler(s, fileService, grants))
	s.AddTool(fsKnownDirsTool(), fsKnownDirsHandler(fileService))

	return s
}

func fsListTool(fs *service.FileService) mcpsdk.Tool {
	return mcpsdk.NewTool("fs.list",
		mcpsdk.WithDescription(
			"[fsRead] Lists the entries of a directory on the LOCAL machine, "+
				"inside the sandbox root ("+fs.Root()+"). `path` is relative to "+
				"that root (default \".\" = the root itself), or an ABSOLUTE path "+
				"inside a folder the user granted via fs.request_access. Paths "+
				"that escape the sandbox and all granted folders (via .., an "+
				"absolute path, or a symlink pointing outside) are refused. "+
				"Read-only.",
		),
		mcpsdk.WithString("path",
			mcpsdk.Description("Directory to list, relative to the sandbox root. Defaults to \".\" (the root)."),
		),
		mcpsdk.WithReadOnlyHintAnnotation(true),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(true),
	)
}

func fsListHandler(fs *service.FileService) func(context.Context, mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	return func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		path := req.GetString("path", ".")

		entries, err := fs.List(path)
		if err != nil {
			return sandboxAwareError("list", path, err), nil
		}
		return mcpsdk.NewToolResultText(formatListing(path, entries)), nil
	}
}

func fsReadTool(fs *service.FileService) mcpsdk.Tool {
	return mcpsdk.NewTool("fs.read",
		mcpsdk.WithDescription(
			"[fsRead] Reads a text file on the LOCAL machine, inside the sandbox "+
				"root ("+fs.Root()+"). `path` is required and relative to the "+
				"root, or an ABSOLUTE path inside a folder the user granted via "+
				"fs.request_access. Returns UTF-8 text; a non-text (binary) file "+
				"is refused with an error rather than returned as garbled "+
				"content. Output is limited to `limit` lines (default 1000) "+
				"starting at line `offset` — a truncated result says which "+
				"offset to continue from; read only the range you need. Paths "+
				"outside the sandbox and all granted folders are refused. "+
				"Read-only.",
		),
		mcpsdk.WithString("path",
			mcpsdk.Required(),
			mcpsdk.Description("File to read, relative to the sandbox root."),
		),
		mcpsdk.WithNumber("offset",
			mcpsdk.Description("1-indexed line to start reading from (default 1). Use with limit to page through big files."),
		),
		mcpsdk.WithNumber("limit",
			mcpsdk.Description("Max lines to return (default 1000)."),
		),
		mcpsdk.WithNumber("max_bytes",
			mcpsdk.Description("Max bytes to read from disk (default 10MB, hard ceiling 50MB). Rarely needed — use offset/limit to control output size."),
		),
		mcpsdk.WithReadOnlyHintAnnotation(true),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(true),
	)
}

func fsReadHandler(fs *service.FileService) func(context.Context, mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	return func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		path, err := req.RequireString("path")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		maxBytes := int64(req.GetInt("max_bytes", 0))
		offset := req.GetInt("offset", 1)
		limit := req.GetInt("limit", 0)

		res, err := fs.Read(path, maxBytes)
		if err != nil {
			return sandboxAwareError("read", path, err), nil
		}
		return mcpsdk.NewToolResultText(formatRead(path, res, offset, limit)), nil
	}
}

func fsEditTool(fs *service.FileService) mcpsdk.Tool {
	return mcpsdk.NewTool("fs.edit",
		mcpsdk.WithDescription(
			"[fsWrite] Replaces old_string with new_string inside a text file "+
				"on the LOCAL machine, inside the sandbox root ("+fs.Root()+") "+
				"or an ABSOLUTE path inside a folder the user granted via "+
				"fs.request_access. old_string must match the file's CURRENT "+
				"content exactly once, unless replace_all is true — 0 matches or "+
				"2+ matches without replace_all are refused with a clear error "+
				"instead of guessing which occurrence to change. This is a plain "+
				"find/replace, not a diff/patch: pass the exact text to remove "+
				"and the exact text to insert (use fs.read first to see the "+
				"file's current content and line numbers). Paths outside the "+
				"sandbox and all granted folders are refused. Mutates the "+
				"filesystem.",
		),
		mcpsdk.WithString("path",
			mcpsdk.Required(),
			mcpsdk.Description("File to edit, relative to the sandbox root."),
		),
		mcpsdk.WithString("old_string",
			mcpsdk.Required(),
			mcpsdk.Description("Exact text to find. Must match exactly once in the file unless replace_all is true."),
		),
		mcpsdk.WithString("new_string",
			mcpsdk.Required(),
			mcpsdk.Description("Exact text to replace old_string with. May be empty to delete old_string."),
		),
		mcpsdk.WithBoolean("replace_all",
			mcpsdk.Description("Replace every occurrence of old_string instead of requiring exactly one (default false)."),
		),
		mcpsdk.WithReadOnlyHintAnnotation(false),
		mcpsdk.WithDestructiveHintAnnotation(true),
		mcpsdk.WithIdempotentHintAnnotation(false),
		mcpsdk.WithOpenWorldHintAnnotation(true),
	)
}

func fsEditHandler(fs *service.FileService) func(context.Context, mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	return func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		path, err := req.RequireString("path")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		oldString, err := req.RequireString("old_string")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		newString, err := req.RequireString("new_string")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		replaceAll := req.GetBool("replace_all", false)

		res, err := fs.Edit(path, oldString, newString, replaceAll)
		if err != nil {
			return sandboxAwareError("edit", path, err), nil
		}
		return mcpsdk.NewToolResultText(formatEdit(path, res)), nil
	}
}

// formatEdit devolve só "path (N replacements)": o modelo JÁ tem old/new nos
// argumentos da tool_call — ecoá-los de volta como diff duplicava esses bytes
// no histórico da conversa pra sempre (args + result, os dois re-enviados a
// cada turno). O diff visual que o app mostra é reconstruído client-side a
// partir dos args (barrakuda-software, bolha de tool result de fs.edit).
func formatEdit(path string, res domain.EditResult) string {
	plural := ""
	if res.Replacements != 1 {
		plural = "s"
	}
	return fmt.Sprintf("%s (%d replacement%s)", path, res.Replacements, plural)
}

func fsSearchTool(fs *service.FileService) mcpsdk.Tool {
	return mcpsdk.NewTool("fs.search",
		mcpsdk.WithDescription(
			"[fsRead] Searches recursively for a regular-expression pattern "+
				"across text files on the LOCAL machine, starting at `path` "+
				"(default \".\" = the sandbox root "+fs.Root()+"), inside the "+
				"sandbox root or an ABSOLUTE path inside a folder the user "+
				"granted via fs.request_access. Returns matching lines as "+
				"`path:line: text`. Binary files and files over the read size "+
				"cap are skipped, not erred. `case_sensitive` defaults to true. "+
				"Results are capped at `max_results` (default 200, hard "+
				"ceiling 1000); hitting the cap stops the search early and "+
				"flags the result as truncated. An invalid pattern is refused "+
				"before any file is read. Read-only.",
		),
		mcpsdk.WithString("pattern",
			mcpsdk.Required(),
			mcpsdk.Description("Regular expression to search for, one line at a time."),
		),
		mcpsdk.WithString("path",
			mcpsdk.Description("Directory (or file) to search, relative to the sandbox root. Defaults to \".\" (the root)."),
		),
		mcpsdk.WithBoolean("case_sensitive",
			mcpsdk.Description("Case-sensitive match (default true)."),
		),
		mcpsdk.WithNumber("max_results",
			mcpsdk.Description("Max matching lines to return (default 200, hard ceiling 1000)."),
		),
		mcpsdk.WithReadOnlyHintAnnotation(true),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(true),
	)
}

func fsSearchHandler(fs *service.FileService) func(context.Context, mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	return func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		pattern, err := req.RequireString("pattern")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		path := req.GetString("path", ".")
		caseSensitive := req.GetBool("case_sensitive", true)
		maxResults := req.GetInt("max_results", 0)

		res, err := fs.Search(path, pattern, caseSensitive, maxResults)
		if err != nil {
			return sandboxAwareError("search", path, err), nil
		}
		return mcpsdk.NewToolResultText(formatSearch(pattern, path, res)), nil
	}
}

func fsWriteTool(fs *service.FileService) mcpsdk.Tool {
	return mcpsdk.NewTool("fs.write",
		mcpsdk.WithDescription(
			"[fsWrite] Writes a text file on the LOCAL machine, inside the "+
				"sandbox root ("+fs.Root()+"), creating it or fully overwriting "+
				"it. `path` and `content` are required; `path` is relative to the "+
				"root, or an ABSOLUTE path inside a folder the user granted via "+
				"fs.request_access. `create_dirs` (default true) controls whether "+
				"missing parent directories are created. Content larger than 10MB "+
				"is refused (not truncated). Paths outside the sandbox and all "+
				"granted folders are refused. Mutates the filesystem.",
		),
		mcpsdk.WithString("path",
			mcpsdk.Required(),
			mcpsdk.Description("File to write, relative to the sandbox root. Overwritten if it exists."),
		),
		mcpsdk.WithString("content",
			mcpsdk.Required(),
			mcpsdk.Description("Text content to write."),
		),
		mcpsdk.WithBoolean("create_dirs",
			mcpsdk.Description("Create missing parent directories (default true). If false, a missing parent makes the write fail."),
		),
		mcpsdk.WithReadOnlyHintAnnotation(false),
		mcpsdk.WithDestructiveHintAnnotation(true),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(true),
	)
}

func fsWriteHandler(fs *service.FileService) func(context.Context, mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	return func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		path, err := req.RequireString("path")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		content, err := req.RequireString("content")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		createDirs := req.GetBool("create_dirs", true)

		res, err := fs.Write(path, content, createDirs)
		if err != nil {
			return sandboxAwareError("write", path, err), nil
		}
		verb := "overwrote"
		if res.Created {
			verb = "created"
		}
		return mcpsdk.NewToolResultText(
			fmt.Sprintf("%s %s (%d bytes written)", verb, path, res.BytesWritten),
		), nil
	}
}

func fsDeleteTool(fs *service.FileService) mcpsdk.Tool {
	return mcpsdk.NewTool("fs.delete",
		mcpsdk.WithDescription(
			"[fsWrite] Deletes a file or empty directory on the LOCAL machine, "+
				"inside the sandbox root ("+fs.Root()+"). `path` is required and "+
				"relative to the root, or an ABSOLUTE path inside a folder the "+
				"user granted via fs.request_access. A non-empty directory is NOT "+
				"deleted (refused) — this never recursively wipes a subtree. "+
				"Deleting the sandbox root or a granted folder itself is refused. "+
				"Paths outside the sandbox and all granted folders are refused. "+
				"Mutates the filesystem.",
		),
		mcpsdk.WithString("path",
			mcpsdk.Required(),
			mcpsdk.Description("File or empty directory to delete, relative to the sandbox root."),
		),
		mcpsdk.WithReadOnlyHintAnnotation(false),
		mcpsdk.WithDestructiveHintAnnotation(true),
		mcpsdk.WithIdempotentHintAnnotation(false),
		mcpsdk.WithOpenWorldHintAnnotation(true),
	)
}

func fsDeleteHandler(fs *service.FileService) func(context.Context, mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	return func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		path, err := req.RequireString("path")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}

		if err := fs.Delete(path); err != nil {
			return sandboxAwareError("delete", path, err), nil
		}
		return mcpsdk.NewToolResultText(fmt.Sprintf("deleted %s", path)), nil
	}
}

func fsRequestAccessTool(grants *service.GrantStore) mcpsdk.Tool {
	return mcpsdk.NewTool("fs.request_access",
		mcpsdk.WithDescription(
			"[fsWrite] Asks the USER for permission to access a folder outside "+
				"the sandbox root. If the user approves, the folder and everything "+
				"under it become fully accessible (list, read, write, delete) to "+
				"the other fs.* tools via ABSOLUTE paths, and the grant persists "+
				"across restarts (stored in "+grants.Path()+"; the user can revoke "+
				"by editing that file). If the user declines, do not retry without "+
				"new instructions. Blocks until the user answers.",
		),
		mcpsdk.WithString("path",
			mcpsdk.Required(),
			mcpsdk.Description("Absolute path of an EXISTING directory to request access to."),
		),
		mcpsdk.WithReadOnlyHintAnnotation(false),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(true),
	)
}

func fsRequestAccessHandler(srv *mcpserver.MCPServer, fs *service.FileService, grants *service.GrantStore) func(context.Context, mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	return func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		path, err := req.RequireString("path")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		if !filepath.IsAbs(path) {
			return mcpsdk.NewToolResultError(
				fmt.Sprintf("request_access refused: %q is not an absolute path", path),
			), nil
		}
		dir, err := filepath.EvalSymlinks(path)
		if err != nil {
			return mcpsdk.NewToolResultError(
				fmt.Sprintf("request_access failed: %q could not be resolved: %s", path, err),
			), nil
		}
		dir = filepath.Clean(dir)
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return mcpsdk.NewToolResultError(
				fmt.Sprintf("request_access refused: %q is not an existing directory", path),
			), nil
		}
		if fs.WithinAllowed(dir) {
			return mcpsdk.NewToolResultText(
				fmt.Sprintf("%s is already accessible; no permission needed", dir),
			), nil
		}

		res, err := srv.RequestElicitation(ctx, mcpsdk.ElicitationRequest{
			Request: mcpsdk.Request{Method: string(mcpsdk.MethodElicitationCreate)},
			Params: mcpsdk.ElicitationParams{
				Message: fmt.Sprintf(
					"The assistant requests full access (read, write, list, delete) to:\n%s\nand everything under it. The grant persists across restarts. Allow?",
					dir,
				),
				// Spec requires a schema for form mode; empty object = plain yes/no,
				// the decision is carried by res.Action.
				RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			},
		})
		if err != nil {
			if errors.Is(err, mcpserver.ErrElicitationNotSupported) || errors.Is(err, mcpserver.ErrNoActiveSession) {
				return mcpsdk.NewToolResultError(
					fmt.Sprintf("request_access failed: the connected client cannot prompt the user for permission (no elicitation support); access to %q cannot be granted", dir),
				), nil
			}
			return mcpsdk.NewToolResultError(
				fmt.Sprintf("request_access failed: %s", err),
			), nil
		}

		if res.Action != mcpsdk.ElicitationResponseActionAccept {
			return mcpsdk.NewToolResultError(
				fmt.Sprintf("the user denied access to %q; do not retry without new instructions from the user", dir),
			), nil
		}
		if err := grants.Add(dir); err != nil {
			return mcpsdk.NewToolResultError(
				fmt.Sprintf("request_access failed: user approved but the grant could not be saved: %s", err),
			), nil
		}
		return mcpsdk.NewToolResultText(
			fmt.Sprintf("access granted to %s; you may now use absolute paths under it with the fs.* tools", dir),
		), nil
	}
}

func fsKnownDirsTool() mcpsdk.Tool {
	return mcpsdk.NewTool("fs.known_dirs",
		mcpsdk.WithDescription(
			"[fsRead] Lists well-known folders of the LOCAL user account (home, "+
				"Desktop, Documents, Downloads) as absolute paths, plus the "+
				"sandbox root. Paths only — no folder contents are read. Use "+
				"this to discover the real path of e.g. the user's Desktop, "+
				"then call fs.request_access with it; folders outside the "+
				"sandbox remain inaccessible until the user approves. "+
				"Read-only, no parameters.",
		),
		mcpsdk.WithReadOnlyHintAnnotation(true),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(true),
	)
}

func fsKnownDirsHandler(fs *service.FileService) func(context.Context, mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	return func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		var b strings.Builder
		fmt.Fprintf(&b, "sandbox root (always accessible, relative paths): %s\n", fs.Root())
		for _, d := range service.KnownDirs() {
			fmt.Fprintf(&b, "%s: %s\n", d.Name, d.Path)
		}
		b.WriteString("folders outside the sandbox require fs.request_access (user approval) before use")
		return mcpsdk.NewToolResultText(b.String()), nil
	}
}

func sandboxAwareError(op, path string, err error) *mcpsdk.CallToolResult {
	switch {
	case errors.Is(err, errorsx.ErrPathOutsideSandbox):
		return mcpsdk.NewToolResultError(
			fmt.Sprintf("%s refused: %q is outside the sandbox root and any user-granted folder. To use this location, call fs.request_access with the absolute path of the folder you need; the user will be asked to approve", op, path),
		)
	case errors.Is(err, errorsx.ErrCannotDeleteRoot):
		return mcpsdk.NewToolResultError(
			fmt.Sprintf("%s refused: cannot delete the sandbox root itself", op),
		)
	case errors.Is(err, errorsx.ErrEmptyPath):
		return mcpsdk.NewToolResultError(fmt.Sprintf("%s failed: path must not be empty", op))
	case errors.Is(err, errorsx.ErrNotFound), errors.Is(err, os.ErrNotExist):
		return mcpsdk.NewToolResultError(fmt.Sprintf("%s failed: %q was not found", op, path))
	case errors.Is(err, errorsx.ErrNotText):
		return mcpsdk.NewToolResultError(
			fmt.Sprintf("%s failed: %q is not valid UTF-8 text (looks binary) and was not returned", op, path),
		)
	case errors.Is(err, errorsx.ErrFileTooLarge):
		return mcpsdk.NewToolResultError(
			fmt.Sprintf("%s failed: %q exceeds the configured size limit", op, path),
		)
	case errors.Is(err, errorsx.ErrNotAFile):
		return mcpsdk.NewToolResultError(fmt.Sprintf("%s failed: %q is not a regular file", op, path))
	case errors.Is(err, errorsx.ErrNotADirectory):
		return mcpsdk.NewToolResultError(fmt.Sprintf("%s failed: %q is not a directory", op, path))
	case errors.Is(err, errorsx.ErrNoMatch):
		return mcpsdk.NewToolResultError(fmt.Sprintf("%s failed: old_string was not found in %q", op, path))
	case errors.Is(err, errorsx.ErrAmbiguousMatch):
		return mcpsdk.NewToolResultError(
			fmt.Sprintf("%s failed: old_string matches more than once in %q — add more surrounding context to make it unique, or pass replace_all=true", op, path),
		)
	default:
		return mcpsdk.NewToolResultError(fmt.Sprintf("%s failed: %s", op, err.Error()))
	}
}

func formatListing(path string, entries []domain.FileEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "listing of %s (%d entries)\n", path, len(entries))
	for _, e := range entries {
		if e.IsDir {
			fmt.Fprintf(&b, "  [dir]  %s\n", e.Name)
		} else {
			fmt.Fprintf(&b, "  [file] %s (%d bytes, modified %s)\n",
				e.Name, e.Size, e.ModTime.Format("2006-01-02 15:04:05"))
		}
	}
	return b.String()
}

// Tetos de output do fs.read — controlam o que entra no CONTEXTO do modelo
// (o cap de disco, max_bytes, segue à parte). Truncou → o rodapé diz de qual
// offset continuar, então ler arquivo grande vira paginação incremental em
// vez de um tool_result gigante que fica no histórico da conversa pra sempre.
const (
	defaultReadLines   = 1000
	maxReadOutputBytes = 48 * 1024
)

func formatRead(path string, res domain.ReadResult, offset, limit int) string {
	lines := strings.Split(res.Content, "\n")
	total := len(lines)
	if offset < 1 {
		offset = 1
	}
	if limit <= 0 {
		limit = defaultReadLines
	}
	if offset > total {
		return fmt.Sprintf("%s has %d lines; offset %d is beyond the end", path, total, offset)
	}
	end := min(offset-1+limit, total)

	// Corpo primeiro: o teto de bytes pode parar antes de `end`, e o header
	// precisa do range realmente emitido.
	// Prefixa cada linha com "N→" (1-indexed) — dá à IA como citar linha e
	// contexto suficiente pra escrever old_string de fs.edit com precisão.
	var body strings.Builder
	last := offset - 1
	for i := offset - 1; i < end; i++ {
		line := fmt.Sprintf("%d→%s\n", i+1, lines[i])
		if body.Len() > 0 && body.Len()+len(line) > maxReadOutputBytes {
			break
		}
		body.WriteString(line)
		last = i + 1
	}

	var b strings.Builder
	if res.Truncated {
		fmt.Fprintf(&b, "[file is %d bytes; only the first %d bytes were read from disk]\n", res.SizeBytes, len(res.Content))
	}
	fmt.Fprintf(&b, "%s (%d bytes, lines %d-%d of %d)\n", path, res.SizeBytes, offset, last, total)
	b.WriteString("--- content ---\n")
	b.WriteString(body.String())
	if last < total {
		fmt.Fprintf(&b, "[TRUNCATED — continue with offset=%d]\n", last+1)
	}
	return b.String()
}

func formatSearch(pattern, path string, res domain.SearchResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "search %q in %s: %d match(es)\n", pattern, path, len(res.Matches))
	if res.Truncated {
		b.WriteString("[TRUNCATED — hit max_results, some matches were not returned]\n")
	}
	for _, m := range res.Matches {
		fmt.Fprintf(&b, "%s:%d: %s\n", m.Path, m.Line, m.Text)
	}
	return b.String()
}
