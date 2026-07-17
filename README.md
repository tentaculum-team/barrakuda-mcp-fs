# barrakuda-mcp-fs

A local [MCP](https://modelcontextprotocol.io/specification) server (stdio,
built on [mark3labs/mcp-go](https://github.com/mark3labs/mcp-go)) that gives an
MCP client real filesystem access — **list, read, search, edit, write,
delete** — on the machine this server runs on, confined to a single sandbox
directory. It is meant
to be spawned locally by the Barrakuda desktop app so the AI chat can work with
files.

> **⚠️ This reads and writes REAL files on the machine that runs it**, within
> the sandbox root described below. Only run it somewhere you accept that.

## Sandbox model (the whole point)

- **Root = the process's current working directory at boot.** When the server
  starts it calls `os.Getwd()` once, canonicalizes it with
  `filepath.EvalSymlinks`, and treats that absolute, symlink-free path as the
  sandbox root. It is resolved **once** and never recomputed. Whoever spawns the
  server (the desktop app) chooses the root by setting the cwd.
- **Every tool path is relative to that root.** A path that would resolve
  outside the root — via `..`, an absolute path, or a symlink pointing out of
  the sandbox — is refused with a structured error (`ErrPathOutsideSandbox`) and
  **no filesystem operation happens**.
- **How the confinement is enforced** (`internal/service`, `resolvePath`):
  1. Empty paths are rejected.
  2. Absolute input paths are rejected outright — the contract is
     "relative to the root", so an absolute path is only ever an escape attempt.
  3. The path is `filepath.Join`ed to the root (which also `Clean`s it, so `..`
     segments collapse here).
  4. **Directory-boundary check, not naive prefix match.** We require the result
     to be the root itself or to start with `root + os.PathSeparator`. A bare
     `strings.HasPrefix(resolved, root)` is deliberately **not** used: it has a
     well-known bypass where root `C:\work` wrongly accepts a sibling
     `C:\work-evil`. Comparison is case-insensitive on Windows (case-insensitive
     filesystem), case-sensitive elsewhere.
  5. **Symlink check.** The lexical check above cannot see symlinks, so we then
     resolve symlinks (`filepath.EvalSymlinks`) on the longest **existing**
     ancestor of the target and re-run the boundary check on the real path. A
     symlink inside the sandbox that points outside is thus refused. For an
     `fs.write` creating a new file (target doesn't exist yet), the nearest
     existing parent is validated instead, and any not-yet-existing suffix is
     re-attached and re-checked.
  6. `fs.delete` additionally refuses to delete the sandbox root itself.

## Tools

8 tools, in two documented permission groups (the names carry the intent;
real per-group enforcement lives in `barrakuda-server`'s extension catalog,
not in this repo — see `manifest.json` for the schema-valid description of
every tool below):

**`fsRead` (read-only):**
- **`fs.list`** — `path` (relative, default `.`) → directory entries
  (directories first, then files by name).
- **`fs.read`** — `path` (required), `max_bytes` (optional) → UTF-8 text
  content, each line prefixed `N→` (1-indexed) so a caller can cite a line
  number or build a precise `fs.edit` `old_string`. See size/text policy
  below.
- **`fs.search`** — `pattern` (required regexp), `path` (optional, default
  `.`), `case_sensitive` (optional, default `true`), `max_results` (optional,
  default 200, hard ceiling 1000) → recursive grep across text files,
  results as `path:line: text`. Binary/oversized files are skipped, not
  erred; an invalid pattern is refused before any file is read.
- **`fs.known_dirs`** — no params → well-known folder paths (home,
  Desktop, Documents, Downloads) plus the sandbox root, for discovering a
  real path to pass to `fs.request_access`.

**`fsWrite` (mutating):**
- **`fs.write`** — `path` (required), `content` (required), `create_dirs`
  (optional, default `true`) → creates or fully overwrites a file. There is no
  separate `fs.mkdir` in v1: `create_dirs` covers directory creation as a side
  effect of writing a file, which is all the chat needs; a standalone mkdir can
  be added later if a real need appears.
- **`fs.edit`** — `path`, `old_string`, `new_string` (all required),
  `replace_all` (optional, default `false`) → find/replace inside an
  existing file. `old_string` must match the file's current content
  exactly once unless `replace_all` is set; 0 or 2+ matches are refused
  rather than guessing which occurrence to change. Plain string
  find/replace, not a diff/patch format — no line counting required.
- **`fs.delete`** — `path` (required) → removes a file or **empty** directory.
- **`fs.request_access`** — `path` (required, absolute) → asks the user
  (via MCP elicitation) to grant full access to a folder outside the
  sandbox root; the grant persists across restarts.

Every failure (sandbox escape, size, IO, missing file, non-text, no/ambiguous
match, invalid pattern) comes back as a structured `NewToolResultError` — the
server never panics. The MCP tool annotations mark the read-only tools as
`readOnlyHint`, and the mutating ones as `destructiveHint`, matching the two
groups.

## Size and text policy

- **Reads are truncated, not rejected.** `fs.read` returns at most the effective
  cap (`max_bytes` if given and within the ceiling, else 10MB default; hard
  ceiling 50MB). If the file is larger, the result is flagged **truncated** and
  reports the full on-disk size — a truncated read is never silently passed off
  as the whole file. A cut that lands mid-rune has its trailing partial UTF-8
  rune trimmed so valid text isn't misreported as binary.
- **Writes over the cap are rejected** (`ErrFileTooLarge`, 10MB), never
  partially or silently truncated — a rejected write is safer than a corrupted
  one.
- **Text detection is deliberately simple** (documented, not sophisticated): a
  file is treated as non-text if it contains a NUL byte or is not valid UTF-8.
  `fs.read` refuses non-text files (`ErrNotText`) rather than returning mojibake.
- **Delete never recurses.** `fs.delete` uses `os.Remove`, so a non-empty
  directory is refused — a single call can never wipe a subtree.

## Layout

- `internal/domain`     — `FileEntry`, `ReadResult`, `WriteResult` (plain data)
- `internal/repository` — `FileRepository`: raw filesystem ops (`os.*`), **no**
  sandbox knowledge; only ever receives already-validated absolute paths
- `internal/service`    — `FileService`: owns the sandbox root and `resolvePath`
  (the security core)
- `internal/mcp`        — MCP server: the 8 tool schemas + thin handlers
- `pkg/errors`          — typed error sentinels (`ErrPathOutsideSandbox`, …)
- `cmd/api`             — wiring + stdio entrypoint
- `manifest.json`       — schema-valid mod manifest (see
  `barrakuda-mod-creator/docs/manifest-schema.md`), the same one the
  Barrakuda app's catalog serves (curator adds `package` pointing at this
  repo's GitHub Release zips); the app installs and runs this like any
  community mod, no special-casing
- `release/`            — per-OS release zips (`make release`, gitignored;
  built via `make_release_zip.py`, then `gh release create` uploads them)

## Build

```
make build          # local binary -> bin/barrakuda-mcp-fs[.exe]
make build-cross    # the 4 Tauri sidecar targets -> bin/barrakuda-mcp-fs-<triple>[.exe]
make lint           # go vet ./...
```

`make` passes `-buildvcs=false` because this tree is not a git repo yet; drop
that flag once it becomes one. Without `make`, use
`go build -buildvcs=false ./...`.

## Try it (manual stdio smoke test)

The server speaks MCP over stdio: JSON-RPC requests (one per line) on stdin,
responses on stdout. The sandbox root is **wherever you launch it from**, so run
it from inside a throwaway directory. Note a real client sends one request and
waits for its response; piping many at once lets the server handle them
concurrently, so responses may come back out of order.

```
mkdir /tmp/fs-sandbox && cd /tmp/fs-sandbox
printf '%s\n' \
 '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"cli","version":"0.0.0"}}}' \
 '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
 '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"fs.write","arguments":{"path":"notes/hello.txt","content":"hi"}}}' \
 '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"fs.read","arguments":{"path":"../secret.txt"}}}' \
 | go run ./cmd/api
```

Request `id:3` writes a file inside the sandbox and succeeds; request `id:4`
tries to read a file above the root and comes back as a structured
sandbox-escape error with nothing accessed.

For real use, point an MCP-aware client (Claude Desktop/Code, the mcp-go
inspector, or the Barrakuda app) at the built binary, launched with its cwd set
to the directory you want to expose as the sandbox.
