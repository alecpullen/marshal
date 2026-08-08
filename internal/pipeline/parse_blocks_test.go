// internal/pipeline/parse_blocks_test.go
package pipeline

import "testing"

func TestParseMarshalBlocksExtractsPatch(t *testing.T) {
	body := "Some prose.\n\n" +
		"```marshal.patch file=\"foo.go\"\n" +
		"<<<<<<< SEARCH\n" +
		"old line\n" +
		"=======\n" +
		"new line\n" +
		">>>>>>> REPLACE\n" +
		"```\n\nMore prose.\n"
	blocks, err := parseMarshalBlocks(body, 0)
	if err != nil {
		t.Fatalf("parseMarshalBlocks: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if blocks[0].kind != "marshal.patch" {
		t.Errorf("kind = %q, want %q", blocks[0].kind, "marshal.patch")
	}
	if blocks[0].attrs["file"] != "foo.go" {
		t.Errorf("file attr = %q, want %q", blocks[0].attrs["file"], "foo.go")
	}
	if !contains(blocks[0].content, "<<<<<<< SEARCH") {
		t.Errorf("content missing SEARCH marker:\n%s", blocks[0].content)
	}
}

func TestParseMarshalBlocksExtractsRun(t *testing.T) {
	body := "```marshal.run\n" +
		"command = [\"go\", \"test\", \"./...\"]\n" +
		"phase = \"verify\"\n" +
		"expect_exit = 0\n" +
		"```\n"
	blocks, err := parseMarshalBlocks(body, 0)
	if err != nil {
		t.Fatalf("parseMarshalBlocks: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if blocks[0].kind != "marshal.run" {
		t.Errorf("kind = %q, want %q", blocks[0].kind, "marshal.run")
	}
}

func TestParseMarshalBlocksExtractsAssert(t *testing.T) {
	body := "```marshal.assert\n" +
		"kind = \"go.symbol\"\n" +
		"file = \"foo.go\"\n" +
		"symbol = \"foo.Bar\"\n" +
		"```\n"
	blocks, err := parseMarshalBlocks(body, 0)
	if err != nil {
		t.Fatalf("parseMarshalBlocks: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if blocks[0].kind != "marshal.assert" {
		t.Errorf("kind = %q, want %q", blocks[0].kind, "marshal.assert")
	}
}

func TestParseMarshalBlocksExtractsAgent(t *testing.T) {
	body := "```marshal.agent\n" +
		"allowed = true\n" +
		"scope = [\"internal/agent\", \"internal/llm\"]\n" +
		"reason = \"plumbing not covered\"\n" +
		"```\n"
	blocks, err := parseMarshalBlocks(body, 0)
	if err != nil {
		t.Fatalf("parseMarshalBlocks: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if blocks[0].kind != "marshal.agent" {
		t.Errorf("kind = %q, want %q", blocks[0].kind, "marshal.agent")
	}
}

func TestParseMarshalBlocksIgnoresNonMarshalBlocks(t *testing.T) {
	body := "```go\npackage foo\n```\n\n```marshal.patch file=\"x.go\"\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n```\n"
	blocks, err := parseMarshalBlocks(body, 0)
	if err != nil {
		t.Fatalf("parseMarshalBlocks: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1 (only marshal blocks)", len(blocks))
	}
}

func TestParseMarshalBlocksMultipleBlocks(t *testing.T) {
	body := "```marshal.patch file=\"a.go\"\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n```\n" +
		"```marshal.run\ncommand = [\"go\", \"test\"]\n```\n"
	blocks, err := parseMarshalBlocks(body, 0)
	if err != nil {
		t.Fatalf("parseMarshalBlocks: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(blocks))
	}
}

func TestParseMarshalBlocksLineRange(t *testing.T) {
	body := "line 1\nline 2\n```marshal.run\ncommand = [\"go\", \"test\"]\n```\nline 6\n"
	blocks, _ := parseMarshalBlocks(body, 0)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	start, end := blocks[0].lineStart, blocks[0].lineEnd
	// The fence opening is on line 3 (0-indexed line 2).
	if start != 2 {
		t.Errorf("lineStart = %d, want 2", start)
	}
	// The fence closing is on line 5 (0-indexed line 4).
	if end != 4 {
		t.Errorf("lineEnd = %d, want 4", end)
	}
}

func TestCompilePatchBlock(t *testing.T) {
	blk := marshalBlock{
		kind:    "marshal.patch",
		attrs:   map[string]string{"file": "foo.go"},
		content: "<<<<<<< SEARCH\nold line\n=======\nnew line\n>>>>>>> REPLACE",
	}
	op, err := compileBlock(blk)
	if err != nil {
		t.Fatalf("compileBlock: %v", err)
	}
	patch, ok := op.(*PatchOp)
	if !ok {
		t.Fatalf("op = %T, want *PatchOp", op)
	}
	if patch.File != "foo.go" {
		t.Errorf("File = %q, want %q", patch.File, "foo.go")
	}
	if patch.Search != "old line" {
		t.Errorf("Search = %q, want %q", patch.Search, "old line")
	}
	if patch.Replace != "new line" {
		t.Errorf("Replace = %q, want %q", patch.Replace, "new line")
	}
}

func TestCompileRunBlock(t *testing.T) {
	blk := marshalBlock{
		kind:    "marshal.run",
		attrs:   map[string]string{},
		content: "command = [\"go\", \"test\", \"./...\"]\nphase = \"verify\"\nexpect_exit = 0",
	}
	op, err := compileBlock(blk)
	if err != nil {
		t.Fatalf("compileBlock: %v", err)
	}
	run, ok := op.(*RunOp)
	if !ok {
		t.Fatalf("op = %T, want *RunOp", op)
	}
	if len(run.Command) != 3 || run.Command[0] != "go" {
		t.Errorf("Command = %v, want [go test ./...]", run.Command)
	}
	if run.Phase != "verify" {
		t.Errorf("Phase = %q, want %q", run.Phase, "verify")
	}
	if run.ExpectExit != 0 {
		t.Errorf("ExpectExit = %d, want 0", run.ExpectExit)
	}
}

func TestCompileAssertBlock(t *testing.T) {
	blk := marshalBlock{
		kind:    "marshal.assert",
		attrs:   map[string]string{},
		content: "kind = \"go.symbol\"\nfile = \"foo.go\"\nsymbol = \"foo.Bar\"",
	}
	op, err := compileBlock(blk)
	if err != nil {
		t.Fatalf("compileBlock: %v", err)
	}
	assert, ok := op.(*AssertOp)
	if !ok {
		t.Fatalf("op = %T, want *AssertOp", op)
	}
	if assert.Kind != AssertGoSymbol {
		t.Errorf("Kind = %q, want %q", assert.Kind, AssertGoSymbol)
	}
	if assert.File != "foo.go" {
		t.Errorf("File = %q, want %q", assert.File, "foo.go")
	}
	if assert.Symbol != "foo.Bar" {
		t.Errorf("Symbol = %q, want %q", assert.Symbol, "foo.Bar")
	}
}

func TestCompileAgentBlock(t *testing.T) {
	blk := marshalBlock{
		kind:    "marshal.agent",
		attrs:   map[string]string{},
		content: "allowed = true\nscope = [\"internal/agent\", \"internal/llm\"]\nreason = \"plumbing\"",
	}
	op, err := compileBlock(blk)
	if err != nil {
		t.Fatalf("compileBlock: %v", err)
	}
	agent, ok := op.(*AgentOp)
	if !ok {
		t.Fatalf("op = %T, want *AgentOp", op)
	}
	if len(agent.AllowedFiles) != 2 {
		t.Errorf("AllowedFiles = %v, want 2 entries", agent.AllowedFiles)
	}
	if agent.Reason != "plumbing" {
		t.Errorf("Reason = %q, want %q", agent.Reason, "plumbing")
	}
}

func TestCompileFileBlock(t *testing.T) {
	blk := marshalBlock{
		kind:    "marshal.file",
		attrs:   map[string]string{"path": "new.go"},
		content: "package new\n",
	}
	op, err := compileBlock(blk)
	if err != nil {
		t.Fatalf("compileBlock: %v", err)
	}
	file, ok := op.(*FileOp)
	if !ok {
		t.Fatalf("op = %T, want *FileOp", op)
	}
	if file.Path != "new.go" {
		t.Errorf("Path = %q, want %q", file.Path, "new.go")
	}
	if file.Content != "package new\n" {
		t.Errorf("Content = %q, want %q", file.Content, "package new\n")
	}
}

func TestCompileUnknownKindReturnsError(t *testing.T) {
	blk := marshalBlock{
		kind:    "marshal.unknown",
		attrs:   map[string]string{},
		content: "foo = bar",
	}
	_, err := compileBlock(blk)
	if err == nil {
		t.Fatal("compileBlock should error on unknown kind")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && indexOf(s, substr) >= 0)
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
