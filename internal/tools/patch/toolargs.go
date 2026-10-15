package patch

// FromToolArgs returns the file patches a write tool call describes, so
// callers that need the target paths or edit chunks (approval preview,
// permission subjects, changed-files tracking, failure hints) can treat
// file.edit and file.write_patch alike. file.edit chunks are built from the
// structured fields directly — never round-tripped through patch text,
// whose delimiters a literal "=======" line in the content would corrupt.
func FromToolArgs(toolName string, args map[string]any) ([]FilePatch, bool) {
	switch toolName {
	case "file.edit":
		path, _ := args["path"].(string)
		old, _ := args["old_string"].(string)
		repl, _ := args["new_string"].(string)
		if path == "" || old == "" {
			return nil, false
		}
		return []FilePatch{{Path: path, Chunks: []PatchChunk{{Search: old, Replace: repl}}}}, true
	case "file.write_patch":
		text, _ := args["patch"].(string)
		patches, err := Parse(text)
		if err != nil || len(patches) == 0 {
			return nil, false
		}
		return patches, true
	}
	return nil, false
}
