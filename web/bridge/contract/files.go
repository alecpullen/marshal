package contract

// The read-only file browser: GET /api/agents/{id}/files?path=… for one
// directory, GET /api/agents/{id}/file?path=… for one file. The bridge
// relays the agent's own answer, so these declarations are that wire, field
// for field. The tree is confined to the agent's workspace: a path outside
// it is refused before it is read.

// FileEntry is one directory entry.
type FileEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
}

// FileList is one directory's entries. Root is the workspace directory the
// paths are relative to, and Path is the directory asked for, empty for the
// root. Directories come first, then names, both in order.
type FileList struct {
	Root    string      `json:"root"`
	Path    string      `json:"path"`
	Entries []FileEntry `json:"entries"`
}

// FileContent is one regular file's content, capped by the agent. A Binary
// file carries no Content, and a Truncated one carries its first megabyte.
type FileContent struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content"`
}
