package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// The repo's own project skills live in .marshal/skills (force-added to
// git; the directory is gitignored). Guard that every one of them parses
// and that using-worktrees is among them.
func TestProjectSkillsParse(t *testing.T) {
	dir := filepath.Join("..", "..", ".marshal", "skills")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skipf("project skills dir %s not present in this checkout", dir)
		}
		t.Fatalf("read project skills dir: %v", err)
	}
	idx := NewIndex()
	count := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		skill, err := Parse(string(raw))
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		idx.Set(skill.Name, skill)
		count++
	}
	if count == 0 {
		t.Fatal("no project skills found")
	}
	if _, ok := idx.Load("using-worktrees"); !ok {
		t.Fatal("using-worktrees not found in project skills index")
	}
}
