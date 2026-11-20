<!-- tools: file.read, repo.search, symbols, codebase_search, transcript_read, image.view -->

# Reading and searching

These tools answer "what does this code say?" and "where is it?". They are not
interchangeable, and the corpus shows agents reaching for the wrong one — most
often `shell.run` with `grep`, or `repo.search` when they wanted a whole file.
`image.view` is the odd one out: the same habit of looking before answering, but
the subject is a picture rather than a file of text.

The boundary, in one line each:

| Tool | Answers |
|---|---|
| `repo.search` | which lines match this string or pattern |
| `file.read` | what does this file say |
| `symbols` | where is this declaration, and who uses it |
| `codebase_search` | which code is about this concept |
| `transcript_read` | what happened earlier in this session |
| `image.view` | what does this picture actually show |

## file.read

**For:** reading a file's contents, whole or by line range.

**Surface** (`internal/tools/native/file.go:86`), risk `read_only`:

| Arg | Required | Notes |
|---|---|---|
| `path` | yes | relative to the workspace, or an absolute path contained in a readable root |
| `start_line` | no | 1-based, inclusive |
| `end_line` | no | 1-based, inclusive |

Lines are clipped at 1500 characters. A ranged read over the byte budget is
truncated with a footer rather than refused.

`path` resolves from the workspace root this request is bound to — the worktree
root while the session is isolated, not the directory Marshal was launched
from. An absolute path is accepted when it resolves inside a root the *read*
operation is granted: the bound workspace root, an explicitly configured
additional root, a named alias (`@run/plan.md`), or the project checkout a
worktree session may consult as a reference. A read grant does not make a root
writable, and the two are judged separately.

A `~` prefix expands to the home directory before resolution. Symlinks are
resolved before the containment check, so a link inside an allowed root cannot
pivot a read elsewhere on disk.

**Call sequence:** `repo.search` or `symbols` to find the site → `file.read` with
`start_line`/`end_line` to read it → `file.edit` to change it → `shell.run` to
verify.

**Failure modes:**

- `file.read path is required` (`file.go:99`)
- `file.read start_line must be <= end_line` (`file.go:102`)
- `%w: %s is too large to read (%d bytes; limit %d)` (`file.go:431`) — an
  unbounded read of an oversized file falls back to the head with a
  continuation hint.

The oversized-range path (`file.go:105-115`) is what makes that hint honest: a
ranged read of an oversized file streams the requested window, so
`file.read start_line=N+1` actually advances instead of returning the same head
again.

## repo.search

**For:** finding which lines in the workspace match a string or pattern.

**Surface** (`internal/tools/native/search.go:41`), risk `read_only`:

| Arg | Required | Notes |
|---|---|---|
| `query` | yes | |
| `path` | no | directory or file to search; resolves by the same rules as `file.read` |
| `max_results` | no | default and hard cap 200 |
| `mode` | no | `auto` (default), `substring`, `regex` |
| `include` | no | glob filter |
| `context` | no | 0-3 surrounding lines |

`path` shares `file.read`'s resolver exactly, so a filter that works for one
works for the other: the bound workspace root, a permitted absolute path, or a
named alias. An absolute spelling alone does not require System access; it still
has to land inside a readable root.

`mode=auto` treats a pattern-shaped query as RE2 when it compiles, and falls
back to a literal substring match silently when it does not. A query that looks
like a regex but does not compile is a soft error by design: the call succeeds
as a substring search rather than failing.

**Call sequence:** `repo.search` to locate → `file.read` to read the whole file
around the hit. `repo.search` returns matching lines, not files.

**Failure modes:**

- `repo.search query is required` (`search.go:55`)
- `repo.search invalid regex %q: %w` (`search.go:89`) — only in explicit
  `mode=regex`
- `repo.search mode must be auto|substring|regex, got %q` (`search.go:94`)

**The clamp.** An out-of-range `context` is coerced into `[0,3]` and reported as
a `context_clamped` notice (`search.go:97-115`; notice kind
`registry.NoticeContextClamped`, `internal/tools/registry/types.go:94`) instead
of erroring. The comment states why:

```go
		// Out-of-range context is clamped into the supported [0, 3] window
		// rather than erroring: postmortems showed agents repeatedly re-issuing
		// the same over-cap call after the hard error, so the call now succeeds
		// and a context_clamped notice carries the remediation instead.
```

That is the general pattern for argument errors in this surface: when the
corpus shows agents retrying the same rejected call, the fix is to accept the
call and carry the correction in a notice.

## symbols

**For:** declaration and reference lookup — "where is this defined", "who calls
it".

**Surface** (`internal/tools/native/symbols.go:26`), risk `read_only`:

| Arg | Required | Notes |
|---|---|---|
| `name` | yes | may be qualified as `Type.Method` |
| `op` | no | `find`, `definition` (default), `references`, `hover` |
| `path` | no | pin one occurrence |
| `line` | no | pin one occurrence, with `path` |

Answers through the language server first, falling back to the symbol index.
Registered only when a querier is wired (`native.go:238-241`) — with no querier
it could only fail, so it is not offered.

**Call sequence:** `symbols` with `op=references` before a refactor to find every
call site; `symbols` with `op=definition` to jump to a declaration without
reading the whole file.

## codebase_search

**For:** finding code by concept when you do not know the identifier — "where is
retry backoff handled".

**Surface** (`internal/tools/native/codebase_search.go:24`), risk `read_only`:

| Arg | Required | Notes |
|---|---|---|
| `query` | yes | natural language |
| `limit` | no | |
| `path` | no | |

Registered only when an embedding route resolves (`native.go:230-233`). For
literal strings use `repo.search`; this is for concepts.

**Failure modes:** none — when no embedder is configured the handler returns
content, not an error (`codebase_search.go:42`):

> Semantic search is not configured: no embedding model configured. Set an
> `embedding` role in your profile to enable it.

A second content path covers the configured-but-not-yet-indexed case: "No
semantic index yet — the background indexer builds it once embeddings are
configured."

## transcript_read

**For:** this session's own history — turns aged out of the replayed context, and
archived turns across the project.

**Surface** (`internal/tools/native/transcript.go:50`), risk `read_only`:

| Arg | Required | Notes |
|---|---|---|
| `query` | no | search archived turns for a distinctive phrase; ignores paging |
| `limit_matches` | no | |
| `generation_seq` | no | archived generation to read; 0 is the first |
| `session_id` | no | defaults to the current session |
| `turn_seq` | no | one turn by its header number |
| `offset` | no | turns to skip |
| `limit` | no | default 20, max 100 |
| `max_chars` | no | default 16000, max 64000 |
| `include_all` | no | live only: include non-replayed narration and notes |

`generation_seq` and `turn_seq` are pointers rather than plain ints so that an
explicit `0` is distinguishable from an omitted field (`transcript.go:59-64`):
generation 0 is the first generation, and archived turn numbers are 0-based, so
a plain int would make both unreachable.

**Call sequence:** `transcript_read` with `query` to find an earlier decision or
command, then `turn_seq` to re-read that turn in full. For source code use
`file.read`, `repo.search`, or `symbols` instead — the transcript is history, not
the repository.

## image.view

**For:** seeing a picture — a screenshot, a diagram, a photo, a chart — instead
of guessing what it shows from its name or from the code that produced it.

**Surface** (`internal/tools/native/image.go:39`), risk `read_only`:

| Arg | Required | Notes |
|---|---|---|
| `path` | one of | image file, resolved the way `file.read` resolves a path |
| `ref` | one of | hash of an image an earlier `image.view` stored |
| `max_edge` | no | longest edge in pixels; defaults to the configured limit |
| `format` | no | `png` or `jpeg`; defaults to the source format, or JPEG when re-encoding |
| `quality` | no | JPEG quality, 1-100 |
| `label` | no | a short label, e.g. `screenshot of /tmp/ui.png` |

Exactly one of `path` and `ref` is required; the schema's `oneOf` refuses both
and neither. PNG, JPEG, GIF (first frame) and WebP sources are accepted. The
bytes are normalised — downscaled to the long edge and stored as PNG, or as JPEG
when PNG does not fit — and the result carries the hash a later turn can name.

**Call sequence:** build or capture the thing → `image.view` on the file it
produced → answer from what the picture actually shows. After a UI change,
viewing the rendered result is the evidence; reading the code that produced it
is not.

The image is part of the conversation from then on: it is replayed with the
transcript until pruning or rollover drops it. A tool result's images are shed
first when the prompt overflows, so an older screenshot costs less than the
user's own.

**Failure modes:**

- `image.view requires one of path or ref` — neither argument was given.
- `image.view takes exactly one of path or ref; both were given`.
- `image.view is unavailable: this session has no image store` — the runtime
  started without one.
- A `ref` that names no stored image fails with the hash.
- The model must be able to take images: on a route with vision off the tool is
  not offered at all, and a prompt carrying an image is refused with a line
  naming the preset to change.
