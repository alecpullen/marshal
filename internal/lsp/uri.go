package lsp

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

func jsonUnmarshal(raw []byte, v any) error {
	return json.Unmarshal(raw, v)
}

// toFileURI converts a possibly-relative file path into a valid file:// URI,
// resolving it against root when necessary and percent-encoding characters
// that are invalid in a URI path (spaces, #, %, etc.).
func toFileURI(root, filePath string) string {
	if !filepath.IsAbs(filePath) {
		if root != "" {
			filePath = filepath.Join(root, filePath)
		} else {
			if abs, err := filepath.Abs(filePath); err == nil {
				filePath = abs
			}
		}
	}
	filePath = filepath.ToSlash(filePath)
	if !strings.HasPrefix(filePath, "/") {
		filePath = "/" + filePath
	}
	return (&url.URL{Scheme: "file", Path: filePath}).String()
}

// fromFileURI converts a file:// URI back to an OS-native absolute path,
// percent-decoding any encoded characters. When root is non-empty it verifies
// the decoded path is lexically contained within the workspace root, rejecting
// absolute or ".."-escaping paths. Note this does not resolve symlinks: a
// symlink inside root that points outside root still passes the lexical check.
func fromFileURI(uri string, root string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return "", fmt.Errorf("not a file URI: %q", uri)
	}
	path := filepath.FromSlash(u.Path)
	if root != "" {
		// Verify the resolved path is within the root.
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return "", fmt.Errorf("path %q not relative to root %q: %w", path, root, err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("path %q is outside workspace root %q", path, root)
		}
	}
	return path, nil
}

func posParams(root, filePath string, line, col int, extra map[string]any) map[string]any {
	p := map[string]any{
		"textDocument": map[string]any{"uri": toFileURI(root, filePath)},
		"position":     map[string]any{"line": line, "character": col},
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

// flattenHoverContents converts the Hover.Contents union (MarkupContent |
// MarkedString | MarkedString[] | string) into a single readable string.
func flattenHoverContents(raw json.RawMessage) string {
	// Try MarkupContent first (most common).
	var mc MarkupContent
	if jsonUnmarshal(raw, &mc) == nil && mc.Value != "" {
		return mc.Value
	}
	// Try MarkedString (object form).
	var ms MarkedString
	if jsonUnmarshal(raw, &ms) == nil && ms.Value != "" {
		return ms.Value
	}
	// Try MarkedString array.
	var msa []MarkedString
	if jsonUnmarshal(raw, &msa) == nil && len(msa) > 0 {
		var b strings.Builder
		for i, m := range msa {
			if i > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(m.Value)
		}
		return b.String()
	}
	// Try bare string.
	var s string
	if jsonUnmarshal(raw, &s) == nil && s != "" {
		return s
	}
	return ""
}
