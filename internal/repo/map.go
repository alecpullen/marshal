package repo

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"marshal/internal/db"
)

// RenderDirectoryMap renders a simple indented directory tree from a file
// index. It shows up to maxFiles file entries; if there are more, it appends
// a truncation note.
func RenderDirectoryMap(files []db.FileIndex, maxFiles int) string {
	if maxFiles <= 0 {
		maxFiles = 200
	}

	tree := &dirNode{name: ".", children: map[string]*dirNode{}}
	for _, f := range files {
		parts := strings.Split(filepath.ToSlash(f.Path), "/")
		insertPath(tree, parts)
	}

	var b strings.Builder
	var fileCount int
	renderNode(&b, tree, "", &fileCount, maxFiles)

	if fileCount > maxFiles {
		fmt.Fprintf(&b, "\n... (%d more files)\n", fileCount-maxFiles)
	}
	return b.String()
}

type dirNode struct {
	name     string
	children map[string]*dirNode
	files    []string
}

func insertPath(node *dirNode, parts []string) {
	if len(parts) == 0 {
		return
	}
	if len(parts) == 1 {
		node.files = append(node.files, parts[0])
		return
	}
	child, ok := node.children[parts[0]]
	if !ok {
		child = &dirNode{name: parts[0], children: map[string]*dirNode{}}
		node.children[parts[0]] = child
	}
	insertPath(child, parts[1:])
}

func renderNode(b *strings.Builder, node *dirNode, prefix string, fileCount *int, maxFiles int) {
	dirs := make([]string, 0, len(node.children))
	for name := range node.children {
		dirs = append(dirs, name)
	}
	sort.Strings(dirs)
	for _, name := range dirs {
		fmt.Fprintf(b, "%s%s/\n", prefix, name)
		renderNode(b, node.children[name], prefix+"  ", fileCount, maxFiles)
	}

	sort.Strings(node.files)
	for _, name := range node.files {
		if *fileCount < maxFiles {
			fmt.Fprintf(b, "%s%s\n", prefix, name)
		}
		*fileCount++
	}
}
