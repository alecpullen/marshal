package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/tools/native"
)

// maxFileView bounds what session/file returns; one extra byte is read to
// detect truncation.
const maxFileView = 1 << 20

// FilesManager serves the read-only file browser under a session's active root.
type FilesManager struct {
	lookup func(string) (*session.State, bool)
}

// NewFilesManager builds a FilesManager over a session lookup.
func NewFilesManager(lookup func(string) (*session.State, bool)) *FilesManager {
	return &FilesManager{lookup: lookup}
}

type filesParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
}

// FileEntry is one directory entry.
type FileEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
}

func (m *FilesManager) resolve(params json.RawMessage, method string) (root, rel, target string, err error) {
	var p filesParams
	if err := decodeParams(params, &p, method); err != nil {
		return "", "", "", err
	}
	st, ok := m.lookup(p.SessionID)
	if !ok || st == nil {
		return "", "", "", serverErrorf("unknown session: %s", p.SessionID)
	}
	root = st.Workspace().ActiveRoot
	if p.Path == "" {
		return root, "", root, nil
	}
	for _, seg := range strings.FieldsFunc(filepath.ToSlash(p.Path), func(r rune) bool { return r == '/' }) {
		if seg == ".git" {
			return "", "", "", invalidParamsError("path is inside .git")
		}
	}
	target, err = native.SafeResolve(root, p.Path)
	if err != nil {
		return "", "", "", invalidParamsError("%v", err)
	}
	return root, p.Path, target, nil
}

// Files handles session/files: one directory's entries, directories first.
func (m *FilesManager) Files(ctx context.Context, params json.RawMessage) (any, error) {
	root, rel, target, err := m.resolve(params, "session/files")
	if err != nil {
		return nil, err
	}
	dirEntries, err := os.ReadDir(target)
	if err != nil {
		return nil, invalidParamsError("read directory: %v", err)
	}
	entries := []FileEntry{}
	for _, de := range dirEntries {
		if de.Name() == ".git" {
			continue
		}
		e := FileEntry{Name: de.Name(), Dir: de.IsDir()}
		if info, err := de.Info(); err == nil && !e.Dir {
			e.Size = info.Size()
		}
		entries = append(entries, e)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Dir != entries[j].Dir {
			return entries[i].Dir
		}
		return entries[i].Name < entries[j].Name
	})
	return map[string]any{"root": root, "path": rel, "entries": entries}, nil
}

// File handles session/file: one regular file's content, capped.
func (m *FilesManager) File(ctx context.Context, params json.RawMessage) (any, error) {
	_, rel, target, err := m.resolve(params, "session/file")
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, invalidParamsError("stat: %v", err)
	}
	if !info.Mode().IsRegular() {
		return nil, invalidParamsError("not a regular file: %s", rel)
	}
	f, err := os.Open(target)
	if err != nil {
		return nil, invalidParamsError("open: %v", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileView+1))
	if err != nil {
		return nil, serverErrorf("read: %v", err)
	}
	truncated := len(data) > maxFileView
	if truncated {
		data = data[:maxFileView]
	}
	head := data
	if len(head) > 8192 {
		head = head[:8192]
	}
	binary := bytes.IndexByte(head, 0) >= 0
	content := ""
	if !binary {
		content = string(data)
	}
	return map[string]any{"path": rel, "size": info.Size(), "binary": binary, "truncated": truncated, "content": content}, nil
}
