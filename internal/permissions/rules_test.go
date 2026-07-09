package permissions

import (
	"testing"
)

func TestMatchesExact(t *testing.T) {
	if !Matches("ls", "ls") {
		t.Fatal("exact match should succeed")
	}
	if Matches("ls", "ls -la") {
		t.Fatal("exact match should not match ls -la")
	}
}

func TestMatchesGlobPrefix(t *testing.T) {
	if !Matches("git *", "git status") {
		t.Fatal("git * should match git status")
	}
	if !Matches("git *", "git push") {
		t.Fatal("git * should match git push")
	}
	if !Matches("git *", "git push --force") {
		t.Fatal("git * should match git push --force")
	}
	if Matches("git *", "got status") {
		t.Fatal("git * should not match got status")
	}
}

func TestMatchesPathGlob(t *testing.T) {
	if !Matches("secrets/**", "secrets/foo/bar.go") {
		t.Fatal("secrets/** should match secrets/foo/bar.go")
	}
	if !Matches("secrets/**", "secrets/x") {
		t.Fatal("secrets/** should match secrets/x")
	}
	if !Matches("secrets/**", "secrets/a/b/c/d.txt") {
		t.Fatal("secrets/** should match deep paths")
	}
	if Matches("secrets/**", "src/main.go") {
		t.Fatal("secrets/** should not match src/main.go")
	}
}

func TestMatchesPathSingleWildcard(t *testing.T) {
	if !Matches("src/*.go", "src/main.go") {
		t.Fatal("src/*.go should match src/main.go")
	}
	if Matches("src/*.go", "src/sub/main.go") {
		t.Fatal("src/*.go should not match src/sub/main.go")
	}
}

func TestMatchesDoubleStarMiddle(t *testing.T) {
	if !Matches("src/**/test", "src/a/b/test") {
		t.Fatal("src/**/test should match src/a/b/test")
	}
	if !Matches("src/**/test", "src/test") {
		t.Fatal("src/**/test should match src/test")
	}
	if Matches("src/**/test", "src/a/b/prod") {
		t.Fatal("src/**/test should not match src/a/b/prod")
	}
}

func TestPermissionMatches(t *testing.T) {
	if !permissionMatches("shell", "shell.run") {
		t.Fatal("shell should match shell.run")
	}
	if !permissionMatches("shell", "test.run") {
		t.Fatal("shell should match test.run")
	}
	if !permissionMatches("mcp.*", "mcp.foo") {
		t.Fatal("mcp.* should match mcp.foo")
	}
	if !permissionMatches("mcp.*", "mcp.bar.baz") {
		t.Fatal("mcp.* should match mcp.bar.baz")
	}
	if permissionMatches("mcp.*", "shell.run") {
		t.Fatal("mcp.* should not match shell.run")
	}
	if !permissionMatches("file.write_patch", "file.write_patch") {
		t.Fatal("exact permission should match")
	}
}

func TestEvaluateLastMatchingWins(t *testing.T) {
	rules := []Rule{
		{Permission: "shell", Pattern: "git *", Action: ActionAllow},
		{Permission: "shell", Pattern: "git push*", Action: ActionAsk},
	}
	action, found := Evaluate(rules, "shell.run", "git push --force")
	if !found {
		t.Fatal("should have found a matching rule")
	}
	if action != ActionAsk {
		t.Fatalf("expected ask, got %s", action)
	}

	action, found = Evaluate(rules, "shell.run", "git status")
	if !found {
		t.Fatal("should have found a matching rule")
	}
	if action != ActionAllow {
		t.Fatalf("expected allow, got %s", action)
	}
}

func TestEvaluateNoMatch(t *testing.T) {
	rules := []Rule{
		{Permission: "shell", Pattern: "git *", Action: ActionAllow},
	}
	_, found := Evaluate(rules, "shell.run", "npm install")
	if found {
		t.Fatal("should not have found a matching rule")
	}
}

func TestEvaluateDeny(t *testing.T) {
	rules := []Rule{
		{Permission: "file.write_patch", Pattern: "secrets/**", Action: ActionDeny},
	}
	action, found := Evaluate(rules, "file.write_patch", "secrets/key.txt")
	if !found {
		t.Fatal("should have found a matching rule")
	}
	if action != ActionDeny {
		t.Fatalf("expected deny, got %s", action)
	}
}

func TestSafeCommandsBareOnly(t *testing.T) {
	for _, r := range SafeCommands {
		if !Matches(r.Pattern, r.Pattern) {
			t.Fatalf("safe command %s should match itself", r.Pattern)
		}
	}
	if Matches("ls", "ls -la") {
		t.Fatal("bare ls should not match ls -la")
	}
	if Matches("cat", "cat file.txt") {
		t.Fatal("bare cat should not match cat file.txt")
	}
}

func TestEvaluateEmptyRules(t *testing.T) {
	_, found := Evaluate(nil, "shell.run", "ls")
	if found {
		t.Fatal("should not match with nil rules")
	}
	_, found = Evaluate([]Rule{}, "shell.run", "ls")
	if found {
		t.Fatal("should not match with empty rules")
	}
}

func TestMatchesPrefixWithoutWildcard(t *testing.T) {
	if !Matches("git", "git") {
		t.Fatal("git should match git")
	}
	if Matches("git", "git status") {
		t.Fatal("bare git should not match git status")
	}
	if Matches("git", "got") {
		t.Fatal("git should not match got")
	}
}
