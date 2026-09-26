package native

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"marshal/internal/app/session"
	"marshal/internal/tools/patch"
	"marshal/internal/tools/registry"
)

// maxPageableFileBytes is the largest file file.page will load into memory.
// It is intentionally larger than the per-tool output limit so the model can
// page through big files a screen at a time.
const maxPageableFileBytes = 10 * 1024 * 1024 // 10 MiB

// Head-fallback budget for file.read when a file exceeds the per-tool size
// limit: show at most this many lines or bytes, whichever comes first.
const (
	oversizedHeadMaxLines = 50
	oversizedHeadMaxBytes = 32 * 1024
)

// maxLineChars is the per-line clip applied to file.read output so a single
// minified or generated line cannot dominate the context window.
const maxLineChars = 1500

// errFileTooLarge marks the "file exceeds the read budget" condition so
// file.read can substitute a head fallback instead of failing outright.
var errFileTooLarge = errors.New("file too large")

// clipLines truncates any single line longer than maxLineChars runes,
// appending an elision marker. The cut is rune-safe (a multi-byte UTF-8 rune
// is never split) and the reported elision count is in runes, matching the
// "chars" in the marker. Returns the clipped text and whether any line was
// cut.
func clipLines(s string) (string, bool) {
	lines := strings.Split(s, "\n")
	clipped := false
	for i, ln := range lines {
		if runes := utf8.RuneCountInString(ln); runes > maxLineChars {
			cut := runeCutIndex(ln, maxLineChars)
			lines[i] = ln[:cut] + " … [" + strconv.Itoa(runes-maxLineChars) + " more chars]"
			clipped = true
		}
	}
	return strings.Join(lines, "\n"), clipped
}

// runeCutIndex returns the byte offset in s at which a cut keeps exactly n
// runes without splitting a multi-byte rune. It returns len(s) when s holds n
// or fewer runes. Only the prefix up to the cut is scanned, so a very long
// line is never converted to []rune just to be clipped.
func runeCutIndex(s string, n int) int {
	if n <= 0 {
		return 0
	}
	count := 0
	for i := range s {
		if count == n {
			return i
		}
		count++
	}
	return len(s)
}

// readPathBaseDescription is the path schema base text for the read
// tools; kept short — schema descriptions are prompt budget.
const readPathBaseDescription = "file path relative to the workspace; absolute paths that resolve inside an allowed root are accepted"

type fileReadArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

func (t *toolSet) fileReadTool() registry.Tool {
	tool := registry.Tool{
		Name:        "file.read",
		Description: "Read a workspace file. If the file exceeds the size limit I show the head and how to continue (file.read start_line / file.page / repo.search kind). Lines are clipped at 1500 chars; ranged reads over the byte budget are truncated with a footer. Use start_line/end_line (1-based, inclusive) to page.",
		Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":` +
			t.pathDescription(readPathBaseDescription) +
			`},"start_line":{"type":"integer","description":"1-based first line to return"},"end_line":{"type":"integer","description":"1-based last line to return"}},"required":["path"],"additionalProperties":false}`),
		Risk: registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[fileReadArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if args.Path == "" {
			return registry.ToolResult{}, fmt.Errorf("file.read path is required")
		}
		if args.StartLine > 0 && args.EndLine > 0 && args.StartLine > args.EndLine {
			return registry.ToolResult{}, fmt.Errorf("file.read start_line must be <= end_line")
		}

		data, err := t.readWorkspaceFile(args.Path, int64(t.maxOutputBytes))
		if err != nil {
			if errors.Is(err, errFileTooLarge) {
				// A ranged read of an oversized file streams the requested
				// window; only an unbounded read falls back to the head. This
				// is what makes the head fallback's advertised continuation
				// (file.read start_line=N+1) actually advance.
				if args.StartLine > 0 || args.EndLine > 0 {
					return t.fileReadOversizedRange(args.Path, args.StartLine, args.EndLine)
				}
				return t.fileReadOversizedHead(args.Path)
			}
			return registry.ToolResult{}, err
		}

		content, start, end := selectLines(string(data), args.StartLine, args.EndLine)
		content, _ = clipLines(content)
		preLimit := len(content)
		content = limitOutput(content, t.maxOutputBytes)

		result := registry.ToolResult{
			Summary: fmt.Sprintf("read %s lines %d-%d", args.Path, start, end),
			Content: content,
		}
		// Detect truncation by comparing the content length around
		// limitOutput rather than testing for the marker suffix: a file whose
		// own content ends with the literal marker would otherwise look
		// truncated when it was not.
		t.applySliceTruncationFooter(&result, preLimit, len(content))
		return result, nil
	}
	return tool
}

// applySliceTruncationFooter appends the output-budget truncation footer and
// attaches the matching NoticeSliceTruncated notice when limitOutput cut the
// content. Truncation is detected by comparing the content length before and
// after limiting (not by testing for the marker suffix, which a file's own
// content could contain), so the footer is appended exactly once and never on
// a false positive. It returns whether truncation occurred.
func (t *toolSet) applySliceTruncationFooter(result *registry.ToolResult, before, after int) bool {
	if before == after {
		return false
	}
	footer := fmt.Sprintf("output truncated at %d bytes; re-issue with a narrower start_line/end_line", t.maxOutputBytes)
	result.Content += "\n" + footer
	result.Notice = &registry.ToolNotice{
		Kind: registry.NoticeSliceTruncated,
		Text: footer,
		Data: map[string]any{"limit_bytes": t.maxOutputBytes},
	}
	return true
}

// fileReadOversizedHead is the fallback for a file that exceeds the read
// budget: instead of failing, it shows the head of the file (at most
// oversizedHeadMaxLines lines or oversizedHeadMaxBytes bytes) plus a footer
// telling the model how to continue. Total line and byte counts are streamed
// so a huge file is never slurped into memory.
func (t *toolSet) fileReadOversizedHead(requestedPath string) (registry.ToolResult, error) {
	path, err := t.resolveToolPath(requestedPath, true)
	if err != nil {
		return registry.ToolResult{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return registry.ToolResult{}, fmt.Errorf("read %s: %w", requestedPath, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return registry.ToolResult{}, fmt.Errorf("stat %s: %w", requestedPath, err)
	}
	totalBytes := info.Size()

	reader := bufio.NewReader(f)
	var head []string
	headBytes := 0
	totalLines := 0
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			totalLines++
			if len(head) < oversizedHeadMaxLines && headBytes < oversizedHeadMaxBytes {
				segment := strings.TrimRight(line, "\r\n")
				if remaining := oversizedHeadMaxBytes - headBytes; len(segment) > remaining {
					segment = segment[:remaining]
				}
				head = append(head, segment)
				headBytes += len(segment)
			}
		}
		if readErr != nil {
			// io.EOF is the normal end of the file; anything else is a real
			// read failure that would otherwise be swallowed and silently
			// under-report total_lines/total_bytes.
			if !errors.Is(readErr, io.EOF) {
				return registry.ToolResult{}, fmt.Errorf("read %s: %w", requestedPath, readErr)
			}
			break
		}
	}

	shown := len(head)
	headText, _ := clipLines(strings.Join(head, "\n"))
	headText = limitOutput(headText, t.maxOutputBytes)

	footer := fmt.Sprintf("showing lines 1-%d of %d (%d bytes); continue with file.read start_line=%d, page with file.page, or narrow with repo.search kind:...",
		shown, totalLines, totalBytes, shown+1)

	content := footer
	if headText != "" {
		content = headText + "\n" + footer
	}

	if t.fileTracker != nil {
		_ = t.fileTracker.RecordRead(path, time.Now())
	}

	return registry.ToolResult{
		Summary: fmt.Sprintf("read %s lines 1-%d of %d (oversized head fallback)", requestedPath, shown, totalLines),
		Content: content,
		Notice: &registry.ToolNotice{
			Kind: registry.NoticeOversizeFallback,
			Text: footer,
			Data: map[string]any{
				"total_lines": totalLines,
				"total_bytes": totalBytes,
				"shown_lines": shown,
			},
		},
	}, nil
}

// fileReadOversizedRange is the fallback for an oversized file when the caller
// supplied start_line/end_line: it streams the requested window instead of
// repeating the head. The file is never slurped — lines are read one at a
// time, lines before the window are skipped, and capturing stops once the
// output byte budget is spent (the remaining lines are only counted, so the
// footer's total stays accurate). Clamping mirrors selectLines: start<=0 → 1,
// end<=0 → EOF, end past EOF → clamp, start past EOF → empty window.
func (t *toolSet) fileReadOversizedRange(requestedPath string, startLine, endLine int) (registry.ToolResult, error) {
	path, err := t.resolveToolPath(requestedPath, true)
	if err != nil {
		return registry.ToolResult{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return registry.ToolResult{}, fmt.Errorf("read %s: %w", requestedPath, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return registry.ToolResult{}, fmt.Errorf("stat %s: %w", requestedPath, err)
	}
	totalBytes := info.Size()

	if startLine <= 0 {
		startLine = 1
	}
	budget := t.maxOutputBytes

	reader := bufio.NewReader(f)
	var window []string
	windowBytes := 0
	budgetExhausted := false
	totalLines := 0
	fileEndsWithNewline := false
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			totalLines++
			fileEndsWithNewline = strings.HasSuffix(line, "\n")
			inWindow := totalLines >= startLine && (endLine <= 0 || totalLines <= endLine)
			if inWindow && !budgetExhausted {
				// Strip only the '\n' ReadString included. selectLines
				// splits on '\n' and keeps a trailing '\r', so a CRLF file
				// must read the same above and below the size limit.
				segment := strings.TrimSuffix(line, "\n")
				window = append(window, segment)
				windowBytes += len(segment) + 1
				if budget > 0 && windowBytes > budget {
					// Budget spent: stop capturing, but keep counting lines
					// below so the footer's total is accurate.
					budgetExhausted = true
				}
			}
		}
		if readErr != nil {
			// io.EOF is the normal end of the file; anything else is a real
			// read failure that must surface rather than silently truncate
			// the reported totals.
			if !errors.Is(readErr, io.EOF) {
				return registry.ToolResult{}, fmt.Errorf("read %s: %w", requestedPath, readErr)
			}
			break
		}
	}

	// Record the read exactly as the head path does: file.write_patch's
	// freshness guard depends on it.
	if t.fileTracker != nil {
		_ = t.fileTracker.RecordRead(path, time.Now())
	}

	// A start past the end of the file is an empty window, not an error —
	// mirroring selectLines' in-budget behavior.
	if startLine > totalLines {
		return registry.ToolResult{
			Summary: fmt.Sprintf("read %s: requested start_line %d is past the end of the file (%d lines)", requestedPath, startLine, totalLines),
			Content: "",
		}, nil
	}

	clampedEnd := endLine
	if clampedEnd <= 0 || clampedEnd > totalLines {
		clampedEnd = totalLines
	}

	body := strings.Join(window, "\n")
	if !budgetExhausted && clampedEnd == totalLines && fileEndsWithNewline {
		// Mirror selectLines: a window that runs to the end of a
		// newline-terminated file keeps the trailing newline.
		body += "\n"
	}
	body, _ = clipLines(body)
	limited := limitOutput(body, budget)

	// The advertised window must describe what was actually emitted, not what
	// was captured. limitOutput cuts the body at exactly `budget` bytes (its
	// marker is appended after the cut), so when a cut happens the emitted
	// prefix is body[:budget] and the line the cut lands in is shown only in
	// part. Counting that line as fully shown would send a caller following the
	// "start_line=shownEnd+1" advice past bytes that were never emitted.
	//
	// A line clipped by clipLines counts as shown: the elision marker states
	// explicitly how much was cut, re-reading yields the same clipped text, and
	// treating it as unshown would stall the continuation on one over-long line
	// forever.
	shownStart := startLine
	shownLines := len(window)
	if budget > 0 && len(body) > budget {
		shownLines = strings.Count(body[:budget], "\n")
	}
	// shownEnd is the last FULLY shown line. It falls below shownStart when the
	// window's first line alone exceeds the whole output budget: that line is
	// emitted partially and the rest of it is not reachable with file.read, so
	// the footer says so instead of advertising a continuation that would skip
	// it.
	shownEnd := shownStart + shownLines - 1

	var windowFooter string
	if shownEnd < shownStart {
		windowFooter = fmt.Sprintf("showing line %d of %d (%d bytes) truncated: a single line is longer than the %d-byte output budget; the rest of that line is not reachable with file.read — extract a byte range with shell.run (e.g. tail -c +N), or narrow with repo.search",
			shownStart, totalLines, totalBytes, budget)
	} else {
		windowFooter = fmt.Sprintf("showing lines %d-%d of %d (%d bytes); continue with file.read start_line=%d, page with file.page, or narrow with repo.search kind:...",
			shownStart, shownEnd, totalLines, totalBytes, shownEnd+1)
	}

	summary := fmt.Sprintf("read %s lines %d-%d of %d (oversized ranged read)", requestedPath, shownStart, shownEnd, totalLines)
	if shownEnd < shownStart {
		summary = fmt.Sprintf("read %s: line %d of %d is longer than the %d-byte output budget (oversized ranged read)",
			requestedPath, shownStart, totalLines, budget)
	}

	result := registry.ToolResult{
		Summary: summary,
		Content: limited + "\n" + windowFooter,
	}
	windowData := map[string]any{
		"start_line":  shownStart,
		"end_line":    shownEnd,
		"shown_lines": shownLines,
		"total_lines": totalLines,
		"total_bytes": totalBytes,
	}
	if t.applySliceTruncationFooter(&result, len(body), len(limited)) {
		// The window was cut by the byte budget as well as being oversized.
		// The slice-truncation notice wins (it is what the in-budget path
		// emits) and carries the window fields too.
		for k, v := range windowData {
			result.Notice.Data[k] = v
		}
	} else {
		result.Notice = &registry.ToolNotice{
			Kind: registry.NoticeOversizeFallback,
			Text: windowFooter,
			Data: windowData,
		}
	}
	return result, nil
}

func (t *toolSet) filePageTool() registry.Tool {
	type filePageArgs struct {
		Path     string `json:"path"`
		Page     int    `json:"page"`
		PageSize int    `json:"page_size"`
	}
	tool := registry.Tool{
		Name:        "file.page",
		Description: "Read a page of a workspace file by 1-based page number. Useful for iterating through large files without spilling tool output.",
		Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":` +
			t.pathDescription(readPathBaseDescription) +
			`},"page":{"type":"integer","description":"1-based page number"},"page_size":{"type":"integer","description":"lines per page (default 200, max 1000)"}},"required":["path","page"],"additionalProperties":false}`),
		Risk: registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[filePageArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if args.Path == "" {
			return registry.ToolResult{}, fmt.Errorf("file.page path is required")
		}
		if args.Page < 1 {
			return registry.ToolResult{}, fmt.Errorf("file.page page must be >= 1")
		}
		pageSize := args.PageSize
		if pageSize <= 0 {
			pageSize = 200
		}
		const maxPageSize = 1000
		if pageSize > maxPageSize {
			pageSize = maxPageSize
		}

		data, err := t.readWorkspaceFile(args.Path, int64(maxPageableFileBytes))
		if err != nil {
			return registry.ToolResult{}, err
		}

		lines := strings.Split(string(data), "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		totalLines := len(lines)
		startLine := (args.Page-1)*pageSize + 1
		if startLine > totalLines {
			return registry.ToolResult{}, fmt.Errorf("file.page page %d is past end of file (total lines %d)", args.Page, totalLines)
		}
		endLine := startLine + pageSize - 1
		if endLine > totalLines {
			endLine = totalLines
		}

		content := strings.Join(lines[startLine-1:endLine], "\n")
		if endLine == totalLines && strings.HasSuffix(string(data), "\n") {
			content += "\n"
		}
		content = limitOutput(content, t.maxOutputBytes)

		return registry.ToolResult{
			Summary: fmt.Sprintf("read %s page %d (lines %d-%d of %d)", args.Path, args.Page, startLine, endLine, totalLines),
			Content: content,
		}, nil
	}
	return tool
}

// readWorkspaceFile resolves and reads a regular workspace file up to maxBytes.
// It performs the same path validation, TOCTOU size check, and read tracking
// used by both file.read and file.page.
func (t *toolSet) readWorkspaceFile(requestedPath string, maxBytes int64) ([]byte, error) {
	path, err := t.resolveToolPath(requestedPath, true)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, t.enrichMissingFileError(requestedPath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", requestedPath)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", requestedPath, err)
	}
	defer f.Close()

	// Re-check size now that we have an open fd; closes the TOCTOU window
	// between os.Stat and the read. A symlink swap after open would change
	// the file backing the fd but the size is fixed at this point.
	info2, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s after open: %w", requestedPath, err)
	}
	if info2.Size() > maxBytes+1 {
		return nil, fmt.Errorf("%w: %s is too large to read (%d bytes; limit %d)",
			errFileTooLarge, requestedPath, info2.Size(), maxBytes)
	}

	cap := maxBytes + 1
	data, err := io.ReadAll(io.LimitReader(f, cap))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", requestedPath, err)
	}

	if t.fileTracker != nil {
		_ = t.fileTracker.RecordRead(path, time.Now())
	}
	return data, nil
}

// changedOnDiskError builds the "file changed on disk" error, embedding the
// current on-disk content's nearest-matching region for the chunk that
// actually went stale (mirroring patch.ValidatePatch's "search block not
// found" hint) so the model can retry the patch immediately instead of
// spending a separate tool-call round-trip re-reading the file first. Live
// testing showed this exact pattern recurring during multi-step edits.
//
// The stale chunk is located rather than assumed: in a multi-hunk patch
// against a file that changed on disk the earlier chunks frequently still
// apply, and showing the model chunk 0's region for a failure that happened
// in chunk 5 points it at content that is not the problem.
func changedOnDiskError(path string, fp patch.FilePatch) error {
	base := fmt.Errorf("file %s changed on disk since last read; re-read it before editing", fp.Path)
	if len(fp.Chunks) == 0 {
		return base
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return base
	}
	search, _, failed := patch.FindFailingChunk(string(data), fp)
	if !failed {
		search = fp.Chunks[0].Search
	}
	region := patch.NearestRegion(string(data), search, patch.NearestRegionWindowSize(search))
	if region == "" {
		return base
	}
	return fmt.Errorf("%s\n\n%s\n%s", base, currentContentHeader, region)
}

// currentContentHeader introduces an embedded snapshot of the bytes currently
// on disk. One spelling is used everywhere a tool result pastes file content
// so postmortem greps match a single vocabulary.
const currentContentHeader = "current on-disk content near the target:"

func (t *toolSet) enrichMissingFileError(requestedPath string, origErr error) error {
	baseErr := fmt.Errorf("stat %s: %w", requestedPath, origErr)
	if t.db == nil || t.projectID == 0 {
		return baseErr
	}

	basename := filepath.Base(requestedPath)
	paths, err := t.db.FilesMatchingBasename(t.projectID, basename, 5)
	if err != nil || len(paths) == 0 {
		return baseErr
	}

	var sb strings.Builder
	sb.WriteString(baseErr.Error())
	sb.WriteString("\n\nclosest indexed paths:\n")
	for _, p := range paths {
		sb.WriteString("  ")
		sb.WriteString(p)
		sb.WriteString("\n")
	}
	return fmt.Errorf("%s", sb.String())
}

func selectLines(content string, startLine int, endLine int) (string, int, int) {
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if startLine <= 0 {
		startLine = 1
	}
	if endLine <= 0 || endLine > len(lines) {
		endLine = len(lines)
	}
	if startLine > len(lines) {
		return "", startLine, endLine
	}
	selected := strings.Join(lines[startLine-1:endLine], "\n")
	if endLine == len(lines) && strings.HasSuffix(content, "\n") {
		selected += "\n"
	}
	return selected, startLine, endLine
}

type fileWritePatchArgs struct {
	Patch string `json:"patch"`
}

func (t *toolSet) fileWritePatchTool() registry.Tool {
	tool := registry.Tool{
		Name: "file.write_patch",
		Description: "Apply one or more search/replace patch blocks to workspace files. " +
			"Each block edits one file; chain multiple blocks in a single call to edit several files at once. " +
			"The SEARCH block must match the current file content exactly. " +
			"To create a new file, use an empty SEARCH block. " +
			"Unified diff format (---/+++/@@ hunks) is also accepted and converted internally. " +
			"Always read before editing a file if you have not already done so this session. " +
			"Every SEARCH/REPLACE block must end with >>>>>>> REPLACE; unified diffs use standard ---/+++/@@ syntax.",
		Schema: json.RawMessage(`{"type":"object","properties":{"patch":{"type":"string","description":` +
			t.pathDescription("search/replace patch blocks; each block's `File:` header is a file path relative to the workspace") +
			`}},"required":["patch"],"additionalProperties":false}`),
		Risk: registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[fileWritePatchArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}

		parsed, err := patch.ParseRepairing(args.Patch)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("parse patch error: %w", err)
		}
		patches, patchRepairs := parsed.Patches, parsed.Repairs
		if len(patches) == 0 {
			return registry.ToolResult{}, fmt.Errorf("no valid patches found in proposal")
		}

		// Dry run first
		for _, fp := range patches {
			path, err := t.resolveToolPath(fp.Path, false)
			if err != nil {
				return registry.ToolResult{}, err
			}

			if t.fileTracker != nil {
				lastRead, hasRead, err := t.fileTracker.LastReadTime(path)
				if err != nil {
					return registry.ToolResult{}, fmt.Errorf(
						"cannot verify read state for %s: %w; re-read it before editing", fp.Path, err)
				}
				info, statErr := os.Stat(path)
				if statErr != nil {
					if os.IsNotExist(statErr) {
						// New file creation: no on-disk version to be stale against.
						continue
					}
					return registry.ToolResult{}, fmt.Errorf("stat %s: %w", fp.Path, statErr)
				}
				if hasRead && info.ModTime().After(lastRead) {
					return registry.ToolResult{}, changedOnDiskError(path, fp)
				}
				if !hasRead {
					return registry.ToolResult{}, fmt.Errorf(
						"file %s was never read this session; read it before editing", fp.Path)
				}
			} else {
				slog.Warn("file.write_patch with nil fileTracker; TOCTOU check skipped",
					"path", fp.Path)
			}

			data, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					// New file creation: no existing content to validate.
					continue
				}
				return registry.ToolResult{}, fmt.Errorf("read file %s: %w", fp.Path, err)
			}
			ok, err := patch.ValidatePatch(string(data), fp)
			if !ok || err != nil {
				return registry.ToolResult{}, fmt.Errorf("patch validation failed for %s: %v", fp.Path, err)
			}
		}

		// Generate diff to display in session
		var diffs []string
		var backups []session.BackupFile
		var symbols []registry.SymbolRef

		// Apply for real
		for _, fp := range patches {
			path, err := t.resolveToolPath(fp.Path, false)
			if err != nil {
				return registry.ToolResult{}, err
			}
			data, err := os.ReadFile(path)
			var original string
			fileExists := err == nil
			if err != nil {
				if !os.IsNotExist(err) {
					return registry.ToolResult{}, err
				}
				// New file creation: verify SEARCH block is empty.
				for _, chunk := range fp.Chunks {
					if chunk.Search != "" {
						return registry.ToolResult{}, fmt.Errorf(
							"file %s does not exist but patch has a non-empty SEARCH block; use an empty SEARCH block to create a new file", fp.Path)
					}
				}
			} else {
				original = string(data)
			}

			info, err := os.Stat(path)
			var mode os.FileMode = 0644
			if err == nil {
				mode = info.Mode()
			}

			diff, err := patch.GenerateDiff(fp.Path, original, fp)
			if err == nil {
				diffs = append(diffs, diff)
			}

			backups = append(backups, session.BackupFile{
				Path:    fp.Path,
				Content: original,
				Mode:    mode,
				Exists:  fileExists,
			})

			patched := patch.ApplyPatch(original, fp)
			if diff != "" {
				symbols = append(symbols, symbolsForEdit(ctx, fp.Path, patched, diff)...)
			}
			if strings.Contains(original, "\r\n") {
				// Normalize safely: collapse existing CRLF to LF first so a
				// naive LF->CRLF conversion does not turn CRLF into CRCRLF.
				patched = strings.ReplaceAll(patched, "\r\n", "\n")
				patched = strings.ReplaceAll(patched, "\n", "\r\n")
			}

			// TOCTOU re-check — verify file hasn't been modified
			// between the validate loop and this write. This closes the window
			// between the fileTracker check in the validate loop and the actual
			// write. When fileTracker is nil (e.g. no session active) the check
			// is skipped, which is a known gap.
			if t.fileTracker != nil {
				lastRead, hasRead, lrErr := t.fileTracker.LastReadTime(path)
				if lrErr == nil && hasRead {
					writeStat, statErr := os.Stat(path)
					if statErr == nil && writeStat.ModTime().After(lastRead) {
						return registry.ToolResult{}, changedOnDiskError(path, fp)
					}
				}
			} else {
				slog.Warn("file.write_patch TOCTOU re-check skipped: nil fileTracker",
					"path", fp.Path)
			}

			if err := os.WriteFile(path, []byte(patched), mode); err != nil {
				return registry.ToolResult{}, fmt.Errorf("write file %s: %w", fp.Path, err)
			}

			if t.fileTracker != nil {
				_ = t.fileTracker.RecordWrite(path, time.Now())
				_ = t.fileTracker.RecordRead(path, time.Now())
			}
		}

		if ws := t.wsState(); ws != nil {
			ws.StoreBackup(backups)
		}

		var paths []string
		for _, fp := range patches {
			paths = append(paths, fp.Path)
		}

		content := strings.Join(diffs, "\n\n")
		// Surface healed format mistakes so the model corrects its next
		// proposal instead of relying on the repair. The patch applied, so
		// this is a notice appended to a success, not an error.
		if len(patchRepairs) > 0 {
			content += "\n\nNote — the proposal's format was repaired before applying:\n- " +
				strings.Join(patchRepairs, "\n- ") +
				"\nClose every REPLACE block with \">>>>>>> REPLACE\"."
		}
		if t.diagnostics != nil {
			diag, _ := t.diagnostics.Check(paths, languageOf(paths))
			if diag != "" {
				content += "\n\n" + diag
			}
		}
		// The in-place edit path is the one the docs convention is most often
		// broken through, so the note rides here too, not just on file.write.
		if notes := specOrPlanDocNudges(paths); len(notes) > 0 {
			content += "\n\n" + strings.Join(notes, "\n")
		}

		return registry.ToolResult{
			Summary:      fmt.Sprintf("Applied patches to: %s", strings.Join(paths, ", ")),
			Content:      content,
			FilesChanged: append([]string(nil), paths...),
			Symbols:      symbols,
		}, nil
	}
	return tool
}

type fileWriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// specOrPlanDocNudge returns the hygiene note for a markdown doc under docs/
// whose name suggests spec/plan/design intent. It fires on the name shape
// alone: whether git tracks the file decides nothing, because a brand-new
// doc, an in-place edit, and a full overwrite are the same mistake — and
// asking git here answers the wrong index in worktree sessions, where the
// write lands in the worktree but the project root holds the index.
func specOrPlanDocNudge(path string) string {
	if !strings.HasPrefix(path, "docs/") || !strings.HasSuffix(path, ".md") {
		return ""
	}
	base := strings.ToLower(filepath.Base(path))
	for _, tok := range []string{"spec", "plan", "design"} {
		if strings.Contains(base, tok) {
			return "note: spec/plan docs belong in .docs-archive/superpowers/{specs,plans}/ (gitignored)."
		}
	}
	return ""
}

// specOrPlanDocNudges returns one path-prefixed note per matching path, in
// write order, deduped so a multi-block patch that touches the same doc once
// per block does not repeat itself.
func specOrPlanDocNudges(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	var notes []string
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		if note := specOrPlanDocNudge(p); note != "" {
			notes = append(notes, p+": "+note)
		}
	}
	return notes
}

// fileWriteTool creates or overwrites a whole file with the exact content
// provided. It shares file.write_patch's plumbing: root resolution, the
// stale-file guard, backups, diff generation, diagnostics, and changed-files
// tracking. New files skip the stale/read guard (no on-disk version to be
// stale against); existing files must have been read this session.
func (t *toolSet) fileWriteTool() registry.Tool {
	tool := registry.Tool{
		Name: "file.write",
		Description: "Create a new file or overwrite an existing file with the exact content provided. " +
			"Prefer this over file.write_patch when writing a whole new file or replacing most of a file's content; " +
			"use file.write_patch for targeted edits. " +
			"Never write files via shell.run redirection or heredocs — those bypass diff review and rollback. " +
			"Always read before overwriting an existing file.",
		Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":` +
			t.pathDescription("file path relative to the workspace") +
			`},"content":{"type":"string","description":"exact file content to write"}},"required":["path","content"],"additionalProperties":false}`),
		Risk: registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[fileWriteArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}

		path, err := t.resolveToolPath(args.Path, false)
		if err != nil {
			return registry.ToolResult{}, err
		}

		// Read the current on-disk state (if any) and enforce the stale-file
		// contract for existing files.
		data, readErr := os.ReadFile(path)
		var original string
		exists := readErr == nil
		if readErr != nil && !os.IsNotExist(readErr) {
			return registry.ToolResult{}, fmt.Errorf("read file %s: %w", args.Path, readErr)
		}
		if exists {
			original = string(data)
			if t.fileTracker == nil {
				slog.Warn("file.write on existing file with nil fileTracker; TOCTOU check skipped",
					"path", args.Path)
				return registry.ToolResult{}, fmt.Errorf(
					"file %s already exists; file.write requires a tracker-backed session to overwrite an existing file", args.Path)
			}
			lastRead, hasRead, lrErr := t.fileTracker.LastReadTime(path)
			if lrErr != nil {
				return registry.ToolResult{}, fmt.Errorf(
					"cannot verify read state for %s: %w; re-read it before editing", args.Path, lrErr)
			}
			info, statErr := os.Stat(path)
			if statErr != nil {
				return registry.ToolResult{}, fmt.Errorf("stat %s: %w", args.Path, statErr)
			}
			if hasRead && info.ModTime().After(lastRead) {
				return registry.ToolResult{}, changedOnDiskError(path, patch.FilePatch{Path: args.Path, Chunks: []patch.PatchChunk{{Search: original}}})
			}
			if !hasRead {
				return registry.ToolResult{}, fmt.Errorf(
					"file %s was never read this session; read it before editing", args.Path)
			}
		}

		info, err := os.Stat(path)
		var mode os.FileMode = 0644
		if err == nil {
			mode = info.Mode()
		}

		// Synthesize a whole-file patch so the in-session diff looks identical
		// to a patch write: empty SEARCH for a new file, whole-file SEARCH
		// otherwise.
		fp := patch.FilePatch{Path: args.Path}
		if exists {
			fp.Chunks = []patch.PatchChunk{{Search: original, Replace: args.Content}}
		} else {
			fp.Chunks = []patch.PatchChunk{{Search: "", Replace: args.Content}}
		}

		content := args.Content
		if strings.Contains(original, "\r\n") {
			// Normalize safely: collapse existing CRLF to LF first so a
			// naive LF->CRLF conversion does not turn CRLF into CRCRLF.
			content = strings.ReplaceAll(content, "\r\n", "\n")
			content = strings.ReplaceAll(content, "\n", "\r\n")
		}

		// Generate the diff using the content that will actually be
		// written, so the displayed diff matches the on-disk bytes. The
		// whole-file patch is synthesized from the normalized content.
		writeFP := patch.FilePatch{Path: args.Path}
		if exists {
			writeFP.Chunks = []patch.PatchChunk{{Search: original, Replace: content}}
		} else {
			writeFP.Chunks = []patch.PatchChunk{{Search: "", Replace: content}}
		}
		var diff string
		if d, dErr := patch.GenerateDiff(args.Path, original, writeFP); dErr == nil {
			diff = d
		}

		// TOCTOU re-check for existing files: verify the file hasn't changed
		// between the read above and this write.
		if t.fileTracker != nil && exists {
			lastRead, hasRead, lrErr := t.fileTracker.LastReadTime(path)
			if lrErr == nil && hasRead {
				writeStat, statErr := os.Stat(path)
				if statErr == nil && writeStat.ModTime().After(lastRead) {
					return registry.ToolResult{}, changedOnDiskError(path, fp)
				}
			}
		}

		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			return registry.ToolResult{}, fmt.Errorf("write file %s: %w", args.Path, err)
		}

		if t.fileTracker != nil {
			_ = t.fileTracker.RecordWrite(path, time.Now())
			_ = t.fileTracker.RecordRead(path, time.Now())
		}

		if ws := t.wsState(); ws != nil {
			ws.StoreBackup([]session.BackupFile{{
				Path:    args.Path,
				Content: original,
				Mode:    mode,
				Exists:  exists,
			}})
		}

		result := registry.ToolResult{
			Summary:      fmt.Sprintf("Wrote %s", args.Path),
			Content:      diff,
			FilesChanged: []string{args.Path},
			Symbols:      symbolsForEdit(ctx, args.Path, content, diff),
		}
		if t.diagnostics != nil {
			diag, _ := t.diagnostics.Check([]string{args.Path}, languageOf([]string{args.Path}))
			if diag != "" {
				result.Content += "\n\n" + diag
			}
		}
		if notes := specOrPlanDocNudges([]string{args.Path}); len(notes) > 0 {
			result.Content += "\n\n" + notes[0]
		}
		return result, nil
	}
	return tool
}
