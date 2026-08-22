package repo

import "testing"

func TestGitignoreStackMatch(t *testing.T) {
	root, err := ParseGitignore("*.log\n")
	if err != nil {
		t.Fatalf("ParseGitignore: %v", err)
	}
	sub, err := ParseGitignore("!important.log\n")
	if err != nil {
		t.Fatalf("ParseGitignore: %v", err)
	}
	stack := NewGitignoreStack(root)
	stack.Push("sub", sub)

	// Root *.log matches debug.log → ignored
	if !stack.Match("debug.log", false) {
		t.Error("stack should ignore debug.log (root *.log)")
	}
	// Root *.log matches, sub !important.log un-ignores → not ignored
	if stack.Match("sub/important.log", false) {
		t.Error("stack should NOT ignore sub/important.log (sub negation overrides root)")
	}
	// Root *.log matches sub/debug.log → ignored
	if !stack.Match("sub/debug.log", false) {
		t.Error("stack should ignore sub/debug.log (root *.log)")
	}
}

func TestGitignoreStackDeepestWins(t *testing.T) {
	root, _ := ParseGitignore("foo.txt\n")
	mid, _ := ParseGitignore("!foo.txt\n")
	deep, _ := ParseGitignore("foo.txt\n")
	stack := NewGitignoreStack(root)
	stack.Push("a", mid)
	stack.Push("a/b", deep)

	// Deepest .gitignore says foo.txt should be ignored
	if !stack.Match("a/b/foo.txt", false) {
		t.Error("deepest .gitignore should win: a/b/foo.txt should be ignored")
	}
}

func TestGitignoreStackPopTo(t *testing.T) {
	root, _ := ParseGitignore("*.log\n")
	sub, _ := ParseGitignore("!test.log\n")
	stack := NewGitignoreStack(root)
	stack.Push("sub", sub)

	// Before pop, sub/test.log is NOT ignored (sub negation)
	if stack.Match("sub/test.log", false) {
		t.Error("sub/test.log should not be ignored before pop")
	}

	// Pop back to root — sub level is removed
	stack.PopTo("")

	// After pop, sub/test.log IS ignored (root *.log, no sub negation)
	if !stack.Match("sub/test.log", false) {
		t.Error("sub/test.log should be ignored after pop removes sub level")
	}
}

func TestGitignoreStackEmpty(t *testing.T) {
	stack := NewGitignoreStack(nil)
	if stack.Match("anything.go", false) {
		t.Error("empty stack should not match anything")
	}
}

func TestGitignoreStackNestedAnchoredPattern(t *testing.T) {
	// A leading-slash pattern in a nested .gitignore is anchored to that
	// .gitignore's directory, matching only direct children.
	sub, err := ParseGitignore("/root.log\n")
	if err != nil {
		t.Fatalf("ParseGitignore: %v", err)
	}
	stack := NewGitignoreStack(&Gitignore{})
	stack.Push("sub", sub)

	if !stack.Match("sub/root.log", false) {
		t.Error("sub/root.log should be ignored by /root.log in sub/.gitignore")
	}
	if stack.Match("sub/deeper/root.log", false) {
		t.Error("sub/deeper/root.log should NOT be ignored by /root.log in sub/.gitignore")
	}

	// A bare pattern (no slash) is unanchored and matches at any depth,
	// just like git.
	unanchored, err := ParseGitignore("any.log\n")
	if err != nil {
		t.Fatalf("ParseGitignore: %v", err)
	}
	stack2 := NewGitignoreStack(&Gitignore{})
	stack2.Push("sub", unanchored)
	if !stack2.Match("sub/any.log", false) {
		t.Error("sub/any.log should be ignored by any.log in sub/.gitignore")
	}
	if !stack2.Match("sub/deeper/any.log", false) {
		t.Error("sub/deeper/any.log should be ignored by any.log in sub/.gitignore")
	}
}
