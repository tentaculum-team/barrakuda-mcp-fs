package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"barrakuda-mcp-fs/internal/domain"
	"barrakuda-mcp-fs/internal/service"
	errorsx "barrakuda-mcp-fs/pkg/errors"
)

const protocolVersion = "2025-06-18"

// transport is the hand-rolled newline-delimited JSON-RPC 2.0 stdio
// connection — no SDK. Its one non-obvious job is elicit: a server-initiated
// request interleaved with the normal client-initiated request/response
// stream on the same pipe. See elicit's doc comment for the concurrency
// assumption that keeps this simple.
type transport struct {
	in                   *bufio.Scanner
	out                  io.Writer
	nextID               int
	elicitationSupported bool
}

func newTransport(in *bufio.Scanner, out io.Writer) *transport {
	return &transport{in: in, out: out}
}

func (t *transport) writeMessage(v map[string]any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = t.out.Write(append(b, '\n'))
	return err
}

// elicit sends a server-initiated "elicitation/create" request and blocks
// reading the very next stdin line as its response.
//
// This is only correct because the real client (barrakuda-software) always
// waits for a tool call's response before sending another one — see
// README.md's "Try it" section — so no other request can land on this
// scanner between the write below and the read that follows. There is
// deliberately no id-correlation map / pending-request bookkeeping like a
// general-purpose MCP SDK needs: a single mod talking to a single
// synchronous client doesn't need to multiplex.
func (t *transport) elicit(message string) (accepted bool, err error) {
	if !t.elicitationSupported {
		return false, errors.New("the connected client did not declare elicitation support at initialize; access cannot be granted")
	}

	t.nextID++
	if err := t.writeMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      t.nextID,
		"method":  "elicitation/create",
		"params": map[string]any{
			"message": message,
			// Spec requires a schema for form mode; empty object = plain
			// yes/no, the decision is carried by result.action.
			"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}); err != nil {
		return false, fmt.Errorf("write elicitation request: %w", err)
	}

	if !t.in.Scan() {
		if err := t.in.Err(); err != nil {
			return false, fmt.Errorf("read elicitation response: %w", err)
		}
		return false, errors.New("read elicitation response: client closed the connection")
	}

	var resp struct {
		Result *struct {
			Action string `json:"action"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(t.in.Bytes(), &resp); err != nil {
		return false, fmt.Errorf("malformed elicitation response: %w", err)
	}
	if resp.Error != nil {
		return false, fmt.Errorf("client returned an error: %s", resp.Error.Message)
	}
	if resp.Result == nil {
		return false, errors.New("malformed elicitation response: missing result")
	}
	return resp.Result.Action == "accept", nil
}

// Serve reads newline-delimited JSON-RPC requests from stdin and writes
// responses to stdout until stdin closes. See transport.elicit for the
// single-request-in-flight assumption this relies on.
func Serve(fs *service.FileService, grants *service.GrantStore) {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024) // fs.write allows up to 10MB of content per request line
	t := newTransport(scanner, os.Stdout)

	tools := allTools(fs, grants)
	handlers := allHandlers(t, fs, grants)

	for t.in.Scan() {
		line := t.in.Bytes()
		if len(line) == 0 {
			continue
		}

		var req map[string]any
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}

		method, _ := req["method"].(string)
		if method == "notifications/initialized" {
			continue
		}

		res := map[string]any{"jsonrpc": "2.0", "id": req["id"]}

		switch method {
		case "initialize":
			params, _ := req["params"].(map[string]any)
			t.elicitationSupported = clientDeclaresElicitation(params)
			res["result"] = map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}, "elicitation": map[string]any{}},
				"serverInfo":      map[string]any{"name": "barrakuda-mcp-fs", "version": "0.4.0"},
			}
		case "tools/list":
			res["result"] = map[string]any{"tools": tools}
		case "tools/call":
			params, _ := req["params"].(map[string]any)
			toolName, _ := params["name"].(string)
			args, _ := params["arguments"].(map[string]any)
			handler, ok := handlers[toolName]
			if !ok {
				res["result"] = errorResult(fmt.Sprintf("unknown tool: %s", toolName))
			} else {
				res["result"] = handler(args)
			}
		default:
			res["error"] = map[string]any{"code": -32601, "message": fmt.Sprintf("method not found: %s", method)}
		}

		if err := t.writeMessage(res); err != nil {
			return
		}
	}
}

func clientDeclaresElicitation(initParams map[string]any) bool {
	caps, _ := initParams["capabilities"].(map[string]any)
	_, ok := caps["elicitation"]
	return ok
}

// ---------------------------------------------------------------------------
// Argument extraction — replaces mcpsdk.CallToolRequest's helpers.
// ---------------------------------------------------------------------------

func getString(args map[string]any, key, def string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return def
}

func requireString(args map[string]any, key string) (string, error) {
	v, ok := args[key].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("missing required argument %q", key)
	}
	return v, nil
}

// getInt reads a numeric argument. JSON numbers decode as float64 in a
// map[string]any, never int.
func getInt(args map[string]any, key string, def int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	return def
}

func getBool(args map[string]any, key string, def bool) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return def
}

func textResult(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
}

func errorResult(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": true}
}

// ---------------------------------------------------------------------------
// Tool registry
// ---------------------------------------------------------------------------

func allTools(fs *service.FileService, grants *service.GrantStore) []map[string]any {
	return []map[string]any{
		fsListTool(fs),
		fsReadTool(fs),
		fsEditTool(fs),
		fsSearchTool(fs),
		fsGlobTool(fs),
		fsWriteTool(fs),
		fsDeleteTool(fs),
		fsRequestAccessTool(grants),
		fsKnownDirsTool(),
	}
}

func allHandlers(t *transport, fs *service.FileService, grants *service.GrantStore) map[string]func(map[string]any) map[string]any {
	return map[string]func(map[string]any) map[string]any{
		"fs.list":           func(args map[string]any) map[string]any { return fsListHandler(t, fs, grants, args) },
		"fs.read":           func(args map[string]any) map[string]any { return fsReadHandler(t, fs, grants, args) },
		"fs.edit":           func(args map[string]any) map[string]any { return fsEditHandler(t, fs, grants, args) },
		"fs.search":         func(args map[string]any) map[string]any { return fsSearchHandler(t, fs, grants, args) },
		"fs.glob":           func(args map[string]any) map[string]any { return fsGlobHandler(t, fs, grants, args) },
		"fs.write":          func(args map[string]any) map[string]any { return fsWriteHandler(t, fs, grants, args) },
		"fs.delete":         func(args map[string]any) map[string]any { return fsDeleteHandler(t, fs, grants, args) },
		"fs.request_access": func(args map[string]any) map[string]any { return fsRequestAccessHandler(t, fs, grants, args) },
		"fs.known_dirs":     func(args map[string]any) map[string]any { return fsKnownDirsHandler(fs, args) },
	}
}

// ---------------------------------------------------------------------------
// fs.list
// ---------------------------------------------------------------------------

func fsListTool(fs *service.FileService) map[string]any {
	return map[string]any{
		"name": "fs.list",
		"description": "[fsRead] Lists the entries of a directory on the LOCAL machine, " +
			"inside the sandbox root (" + fs.Root() + "). `path` is relative to " +
			"that root (default \".\" = the root itself), or an ABSOLUTE path " +
			"anywhere else — first use outside the sandbox asks the user to " +
			"approve (blocks until answered; persists after). Read-only.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Directory to list, relative to the sandbox root. Defaults to \".\" (the root)."},
			},
		},
		"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true},
	}
}

func fsListHandler(t *transport, fs *service.FileService, grants *service.GrantStore, args map[string]any) map[string]any {
	path := getString(args, "path", ".")
	if res := ensureAccess(t, fs, grants, path); res != nil {
		return res
	}

	entries, err := fs.List(path)
	if err != nil {
		return sandboxAwareError("list", path, err)
	}
	return textResult(formatListing(path, entries))
}

// ---------------------------------------------------------------------------
// fs.read
// ---------------------------------------------------------------------------

func fsReadTool(fs *service.FileService) map[string]any {
	return map[string]any{
		"name": "fs.read",
		"description": "[fsRead] Reads a text file on the LOCAL machine, inside the sandbox " +
			"root (" + fs.Root() + "). `path` is required and relative to the " +
			"root, or an ABSOLUTE path anywhere else — first use outside the " +
			"sandbox asks the user to approve (blocks until answered; " +
			"persists after). Returns UTF-8 text; a non-text (binary) file " +
			"is refused with an error rather than returned as garbled " +
			"content. Output is limited to `limit` lines (default 1000) " +
			"starting at line `offset` — a truncated result says which " +
			"offset to continue from; read only the range you need. " +
			"Read-only.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":      map[string]any{"type": "string", "description": "File to read, relative to the sandbox root."},
				"offset":    map[string]any{"type": "number", "description": "1-indexed line to start reading from (default 1). Use with limit to page through big files."},
				"limit":     map[string]any{"type": "number", "description": "Max lines to return (default 1000)."},
				"max_bytes": map[string]any{"type": "number", "description": "Max bytes to read from disk (default 10MB, hard ceiling 50MB). Rarely needed — use offset/limit to control output size."},
			},
			"required": []string{"path"},
		},
		"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true},
	}
}

func fsReadHandler(t *transport, fs *service.FileService, grants *service.GrantStore, args map[string]any) map[string]any {
	path, err := requireString(args, "path")
	if err != nil {
		return errorResult(err.Error())
	}
	if res := ensureAccess(t, fs, grants, path); res != nil {
		return res
	}
	maxBytes := int64(getInt(args, "max_bytes", 0))
	offset := getInt(args, "offset", 1)
	limit := getInt(args, "limit", 0)

	res, err := fs.Read(path, maxBytes)
	if err != nil {
		return sandboxAwareError("read", path, err)
	}
	return textResult(formatRead(path, res, offset, limit))
}

// ---------------------------------------------------------------------------
// fs.edit
// ---------------------------------------------------------------------------

func fsEditTool(fs *service.FileService) map[string]any {
	return map[string]any{
		"name": "fs.edit",
		"description": "[fsWrite] Replaces old_string with new_string inside a text file " +
			"on the LOCAL machine, inside the sandbox root (" + fs.Root() + ") " +
			"or an ABSOLUTE path anywhere else — first use outside the " +
			"sandbox asks the user to approve (blocks until answered; " +
			"persists after). old_string must match the file's CURRENT " +
			"content exactly once, unless replace_all is true — 0 matches or " +
			"2+ matches without replace_all are refused with a clear error " +
			"instead of guessing which occurrence to change. This is a plain " +
			"find/replace, not a diff/patch: pass the exact text to remove " +
			"and the exact text to insert (use fs.read first to see the " +
			"file's current content and line numbers). Mutates the " +
			"filesystem.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":        map[string]any{"type": "string", "description": "File to edit, relative to the sandbox root."},
				"old_string":  map[string]any{"type": "string", "description": "Exact text to find. Must match exactly once in the file unless replace_all is true."},
				"new_string":  map[string]any{"type": "string", "description": "Exact text to replace old_string with. May be empty to delete old_string."},
				"replace_all": map[string]any{"type": "boolean", "description": "Replace every occurrence of old_string instead of requiring exactly one (default false)."},
			},
			"required": []string{"path", "old_string", "new_string"},
		},
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": true},
	}
}

func fsEditHandler(t *transport, fs *service.FileService, grants *service.GrantStore, args map[string]any) map[string]any {
	path, err := requireString(args, "path")
	if err != nil {
		return errorResult(err.Error())
	}
	if res := ensureAccess(t, fs, grants, path); res != nil {
		return res
	}
	oldString, err := requireString(args, "old_string")
	if err != nil {
		return errorResult(err.Error())
	}
	newString, err := requireString(args, "new_string")
	if err != nil {
		return errorResult(err.Error())
	}
	replaceAll := getBool(args, "replace_all", false)

	res, err := fs.Edit(path, oldString, newString, replaceAll)
	if err != nil {
		return sandboxAwareError("edit", path, err)
	}
	return textResult(formatEdit(path, res))
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

// ---------------------------------------------------------------------------
// fs.search
// ---------------------------------------------------------------------------

func fsSearchTool(fs *service.FileService) map[string]any {
	return map[string]any{
		"name": "fs.search",
		"description": "[fsRead] Searches recursively for a regular-expression pattern " +
			"across text files on the LOCAL machine, starting at `path` " +
			"(default \".\" = the sandbox root " + fs.Root() + "), inside the " +
			"sandbox root or an ABSOLUTE path anywhere else — first use " +
			"outside the sandbox asks the user to approve (blocks until " +
			"answered; persists after). Returns matching lines as " +
			"`path:line: text`. Binary files and files over the read size " +
			"cap are skipped, not erred. `case_sensitive` defaults to true. " +
			"Results are capped at `max_results` (default 200, hard " +
			"ceiling 1000); hitting the cap stops the search early and " +
			"flags the result as truncated. An invalid pattern is refused " +
			"before any file is read. Read-only.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern":        map[string]any{"type": "string", "description": "Regular expression to search for, one line at a time."},
				"path":           map[string]any{"type": "string", "description": "Directory (or file) to search, relative to the sandbox root. Defaults to \".\" (the root)."},
				"case_sensitive": map[string]any{"type": "boolean", "description": "Case-sensitive match (default true)."},
				"max_results":    map[string]any{"type": "number", "description": "Max matching lines to return (default 200, hard ceiling 1000)."},
			},
			"required": []string{"pattern"},
		},
		"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true},
	}
}

func fsSearchHandler(t *transport, fs *service.FileService, grants *service.GrantStore, args map[string]any) map[string]any {
	pattern, err := requireString(args, "pattern")
	if err != nil {
		return errorResult(err.Error())
	}
	path := getString(args, "path", ".")
	if res := ensureAccess(t, fs, grants, path); res != nil {
		return res
	}
	caseSensitive := getBool(args, "case_sensitive", true)
	maxResults := getInt(args, "max_results", 0)

	res, err := fs.Search(path, pattern, caseSensitive, maxResults)
	if err != nil {
		return sandboxAwareError("search", path, err)
	}
	return textResult(formatSearch(pattern, path, res))
}

// ---------------------------------------------------------------------------
// fs.glob
// ---------------------------------------------------------------------------

func fsGlobTool(fs *service.FileService) map[string]any {
	return map[string]any{
		"name": "fs.glob",
		"description": "[fsRead] Finds files by NAME pattern recursively on the LOCAL " +
			"machine, starting at `path` (default \".\" = the sandbox root " + fs.Root() + "), " +
			"inside the sandbox root or an ABSOLUTE path anywhere else — first " +
			"use outside the sandbox asks the user to approve (blocks until " +
			"answered; persists after). This matches file NAMES, not content — " +
			"use fs.search for content. `pattern` is shell-glob syntax (`*`, " +
			"`?`, `[...]`), not regex: `\"*.go\"` matches at any depth by base " +
			"name; a pattern containing `/` (e.g. `\"internal/*.go\"`) matches " +
			"against the path relative to `path` instead. Results are capped " +
			"at `max_results` (default 200, hard ceiling 1000); hitting the " +
			"cap stops early and flags the result as truncated. An invalid " +
			"pattern is refused before any file is touched. Read-only.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern":     map[string]any{"type": "string", "description": "Shell-glob pattern to match file names against (e.g. \"*.go\", \"internal/*.go\"). Not regex."},
				"path":        map[string]any{"type": "string", "description": "Directory to search under, relative to the sandbox root. Defaults to \".\" (the root)."},
				"max_results": map[string]any{"type": "number", "description": "Max matching paths to return (default 200, hard ceiling 1000)."},
			},
			"required": []string{"pattern"},
		},
		"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true},
	}
}

func fsGlobHandler(t *transport, fs *service.FileService, grants *service.GrantStore, args map[string]any) map[string]any {
	pattern, err := requireString(args, "pattern")
	if err != nil {
		return errorResult(err.Error())
	}
	path := getString(args, "path", ".")
	if res := ensureAccess(t, fs, grants, path); res != nil {
		return res
	}
	maxResults := getInt(args, "max_results", 0)

	res, err := fs.Glob(path, pattern, maxResults)
	if err != nil {
		return sandboxAwareError("glob", path, err)
	}
	return textResult(formatGlob(pattern, path, res))
}

// ---------------------------------------------------------------------------
// fs.write
// ---------------------------------------------------------------------------

func fsWriteTool(fs *service.FileService) map[string]any {
	return map[string]any{
		"name": "fs.write",
		"description": "[fsWrite] Writes a text file on the LOCAL machine, inside the " +
			"sandbox root (" + fs.Root() + "), creating it or fully overwriting " +
			"it. `path` and `content` are required; `path` is relative to the " +
			"root, or an ABSOLUTE path anywhere else — first use outside the " +
			"sandbox asks the user to approve (blocks until answered; " +
			"persists after). `create_dirs` (default true) controls whether " +
			"missing parent directories are created. Content larger than 10MB " +
			"is refused (not truncated). Mutates the filesystem.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":        map[string]any{"type": "string", "description": "File to write, relative to the sandbox root. Overwritten if it exists."},
				"content":     map[string]any{"type": "string", "description": "Text content to write."},
				"create_dirs": map[string]any{"type": "boolean", "description": "Create missing parent directories (default true). If false, a missing parent makes the write fail."},
			},
			"required": []string{"path", "content"},
		},
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": true, "openWorldHint": true},
	}
}

func fsWriteHandler(t *transport, fs *service.FileService, grants *service.GrantStore, args map[string]any) map[string]any {
	path, err := requireString(args, "path")
	if err != nil {
		return errorResult(err.Error())
	}
	if res := ensureAccess(t, fs, grants, path); res != nil {
		return res
	}
	content, err := requireString(args, "content")
	if err != nil {
		return errorResult(err.Error())
	}
	createDirs := getBool(args, "create_dirs", true)

	res, err := fs.Write(path, content, createDirs)
	if err != nil {
		return sandboxAwareError("write", path, err)
	}
	verb := "overwrote"
	if res.Created {
		verb = "created"
	}
	return textResult(fmt.Sprintf("%s %s (%d bytes written)", verb, path, res.BytesWritten))
}

// ---------------------------------------------------------------------------
// fs.delete
// ---------------------------------------------------------------------------

func fsDeleteTool(fs *service.FileService) map[string]any {
	return map[string]any{
		"name": "fs.delete",
		"description": "[fsWrite] Deletes a file or empty directory on the LOCAL machine, " +
			"inside the sandbox root (" + fs.Root() + "). `path` is required and " +
			"relative to the root, or an ABSOLUTE path anywhere else — first " +
			"use outside the sandbox asks the user to approve (blocks until " +
			"answered; persists after). A non-empty directory is NOT " +
			"deleted (refused) — this never recursively wipes a subtree. " +
			"Deleting the sandbox root or a granted folder itself is refused. " +
			"Mutates the filesystem.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "File or empty directory to delete, relative to the sandbox root."},
			},
			"required": []string{"path"},
		},
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": true},
	}
}

func fsDeleteHandler(t *transport, fs *service.FileService, grants *service.GrantStore, args map[string]any) map[string]any {
	path, err := requireString(args, "path")
	if err != nil {
		return errorResult(err.Error())
	}
	if res := ensureAccess(t, fs, grants, path); res != nil {
		return res
	}

	if err := fs.Delete(path); err != nil {
		return sandboxAwareError("delete", path, err)
	}
	return textResult(fmt.Sprintf("deleted %s", path))
}

// ---------------------------------------------------------------------------
// fs.request_access
// ---------------------------------------------------------------------------

func fsRequestAccessTool(grants *service.GrantStore) map[string]any {
	return map[string]any{
		"name": "fs.request_access",
		"description": "[fsWrite] OPTIONAL pre-authorization: asks the USER for permission " +
			"to access a folder outside the sandbox root, without performing " +
			"any operation on it. Every other fs.* tool already does this " +
			"itself on first use of an absolute path — call this ONLY to " +
			"secure access ahead of time (e.g. before a multi-step task on " +
			"that folder) or to check access without touching anything. If " +
			"the user approves, the folder and everything under it become " +
			"fully accessible (list, read, write, delete) to the other fs.* " +
			"tools via ABSOLUTE paths, and the grant persists across restarts " +
			"(stored in " + grants.Path() + "; the user can revoke by editing that " +
			"file). If the user declines, do not retry without new " +
			"instructions. Blocks until the user answers.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Absolute path of an EXISTING directory to request access to."},
			},
			"required": []string{"path"},
		},
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true},
	}
}

func fsRequestAccessHandler(t *transport, fs *service.FileService, grants *service.GrantStore, args map[string]any) map[string]any {
	path, err := requireString(args, "path")
	if err != nil {
		return errorResult(err.Error())
	}
	if !filepath.IsAbs(path) {
		return errorResult(fmt.Sprintf("request_access refused: %q is not an absolute path", path))
	}
	dir, err := filepath.EvalSymlinks(path)
	if err != nil {
		return errorResult(fmt.Sprintf("request_access failed: %q could not be resolved: %s", path, err))
	}
	dir = filepath.Clean(dir)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return errorResult(fmt.Sprintf("request_access refused: %q is not an existing directory", path))
	}
	if fs.WithinAllowed(dir) {
		return textResult(fmt.Sprintf("%s is already accessible; no permission needed", dir))
	}
	if res := elicitAndGrant(t, grants, dir); res != nil {
		return res
	}
	return textResult(fmt.Sprintf("access granted to %s; you may now use absolute paths under it with the fs.* tools", dir))
}

// ensureAccess is the auto-consent gate every fs.* handler runs before
// touching a path: for an ABSOLUTE path outside the sandbox root and any
// grant, it elicits the user right there in the same tool call instead of
// requiring the model to call fs.request_access as a separate round-trip
// first. Returns nil to let the caller proceed; a non-nil result must be
// returned to the model as-is. Relative paths are untouched (sandbox-only,
// no grant ever applies) and any resolution error here is swallowed — the
// caller's own path handling (fs.List/fs.Read/…) already produces a clear,
// correctly-typed error for those cases (not found, empty path, etc.).
func ensureAccess(t *transport, fs *service.FileService, grants *service.GrantStore, path string) map[string]any {
	if !filepath.IsAbs(path) {
		return nil
	}
	dir, err := fs.ResolveAccessTarget(path)
	if err != nil || dir == "" {
		return nil
	}
	return elicitAndGrant(t, grants, dir)
}

// elicitAndGrant asks the user (via MCP elicitation) for full access to dir
// and persists the grant on approval. nil = approved and saved; non-nil is
// the error/decline result to return to the model verbatim.
func elicitAndGrant(t *transport, grants *service.GrantStore, dir string) map[string]any {
	accepted, err := t.elicit(fmt.Sprintf(
		"The assistant requests full access (read, write, list, delete) to:\n%s\nand everything under it. The grant persists across restarts. Allow?",
		dir,
	))
	if err != nil {
		return errorResult(fmt.Sprintf("access check failed: %s", err))
	}
	if !accepted {
		return errorResult(fmt.Sprintf("the user denied access to %q; do not retry without new instructions from the user", dir))
	}
	if err := grants.Add(dir); err != nil {
		return errorResult(fmt.Sprintf("access check failed: user approved but the grant could not be saved: %s", err))
	}
	return nil
}

// ---------------------------------------------------------------------------
// fs.known_dirs
// ---------------------------------------------------------------------------

func fsKnownDirsTool() map[string]any {
	return map[string]any{
		"name": "fs.known_dirs",
		"description": "[fsRead] Lists well-known folders of the LOCAL user account (home, " +
			"Desktop, Documents, Downloads) as absolute paths, plus the " +
			"sandbox root. Paths only — no folder contents are read. Use " +
			"this to discover the real path of e.g. the user's Desktop, then " +
			"call the fs.* tool you actually need with it directly (fs.write, " +
			"fs.read, …) — first use outside the sandbox asks the user to " +
			"approve inline, no separate step needed. Read-only, no " +
			"parameters.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true},
	}
}

func fsKnownDirsHandler(fs *service.FileService, _ map[string]any) map[string]any {
	var b strings.Builder
	fmt.Fprintf(&b, "sandbox root (always accessible, relative paths): %s\n", fs.Root())
	for _, d := range service.KnownDirs() {
		fmt.Fprintf(&b, "%s: %s\n", d.Name, d.Path)
	}
	b.WriteString("folders outside the sandbox: just call the fs.* tool you need with the absolute path — first use asks the user to approve inline")
	return textResult(b.String())
}

// ---------------------------------------------------------------------------
// Shared error/format helpers — unchanged from the SDK-based version.
// ---------------------------------------------------------------------------

func sandboxAwareError(op, path string, err error) map[string]any {
	switch {
	case errors.Is(err, errorsx.ErrPathOutsideSandbox):
		return errorResult(fmt.Sprintf("%s refused: %q is outside the sandbox root and any user-granted folder. To use this location, call fs.request_access with the absolute path of the folder you need; the user will be asked to approve", op, path))
	case errors.Is(err, errorsx.ErrCannotDeleteRoot):
		return errorResult(fmt.Sprintf("%s refused: cannot delete the sandbox root itself", op))
	case errors.Is(err, errorsx.ErrEmptyPath):
		return errorResult(fmt.Sprintf("%s failed: path must not be empty", op))
	case errors.Is(err, errorsx.ErrNotFound), errors.Is(err, os.ErrNotExist):
		return errorResult(fmt.Sprintf("%s failed: %q was not found", op, path))
	case errors.Is(err, errorsx.ErrNotText):
		return errorResult(fmt.Sprintf("%s failed: %q is not valid UTF-8 text (looks binary) and was not returned", op, path))
	case errors.Is(err, errorsx.ErrFileTooLarge):
		return errorResult(fmt.Sprintf("%s failed: %q exceeds the configured size limit", op, path))
	case errors.Is(err, errorsx.ErrNotAFile):
		return errorResult(fmt.Sprintf("%s failed: %q is not a regular file", op, path))
	case errors.Is(err, errorsx.ErrNotADirectory):
		return errorResult(fmt.Sprintf("%s failed: %q is not a directory", op, path))
	case errors.Is(err, errorsx.ErrNoMatch):
		return errorResult(fmt.Sprintf("%s failed: old_string was not found in %q", op, path))
	case errors.Is(err, errorsx.ErrAmbiguousMatch):
		return errorResult(fmt.Sprintf("%s failed: old_string matches more than once in %q — add more surrounding context to make it unique, or pass replace_all=true", op, path))
	default:
		return errorResult(fmt.Sprintf("%s failed: %s", op, err.Error()))
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

func formatGlob(pattern, path string, res domain.GlobResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "glob %q in %s: %d match(es)\n", pattern, path, len(res.Paths))
	if res.Truncated {
		b.WriteString("[TRUNCATED — hit max_results, some matches were not returned]\n")
	}
	for _, p := range res.Paths {
		fmt.Fprintf(&b, "%s\n", p)
	}
	return b.String()
}
