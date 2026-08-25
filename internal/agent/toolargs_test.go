package agent

import (
	"encoding/json"
	"testing"
)

func TestSummarizeToolArgsShellRun(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"command": "go test ./..."})
	got := SummarizeToolArgs("shell.run", args)
	if got != "go test ./..." {
		t.Fatalf("SummarizeToolArgs(shell.run) = %q, want %q", got, "go test ./...")
	}
}

func TestSummarizeToolArgsTestRun(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"command": "npm test"})
	got := SummarizeToolArgs("test.run", args)
	if got != "npm test" {
		t.Fatalf("SummarizeToolArgs(test.run) = %q, want %q", got, "npm test")
	}
}

func TestSummarizeToolArgsFileRead(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"path": "/repo/main.go"})
	got := SummarizeToolArgs("file.read", args)
	if got != "/repo/main.go" {
		t.Fatalf("SummarizeToolArgs(file.read) = %q, want %q", got, "/repo/main.go")
	}
}

func TestSummarizeToolArgsRepoSearch(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"query": "func main", "pattern": "*.go"})
	got := SummarizeToolArgs("repo.search", args)
	if got != "func main" {
		t.Fatalf("SummarizeToolArgs(repo.search) = %q, want %q", got, "func main")
	}
}

func TestSummarizeToolArgsEmptyArgs(t *testing.T) {
	got := SummarizeToolArgs("unknown.tool", nil)
	if got != "" {
		t.Fatalf("SummarizeToolArgs(nil args) = %q, want empty", got)
	}
}

func TestSummarizeToolArgsPatch(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"patch": "File: a.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"})
	got := SummarizeToolArgs("file.write_patch", args)
	if got != "patch" {
		t.Fatalf("SummarizeToolArgs(file.write_patch) = %q, want the concise label %q", got, "patch")
	}
}

// agent.await's arguments are numeric and boolean, so the default branch's
// "first string value" rule found nothing and the tool line rendered bare —
// a blocking call with no indication of what it was blocked on.
func TestSummarizeToolArgsAgentAwait(t *testing.T) {
	for _, tc := range []struct {
		args string
		want string
	}{
		{`{"id":3}`, "#3"},
		{`{"all":true}`, "all"},
		{`{"any":true}`, "any"},
	} {
		got := SummarizeToolArgs("agent.await", json.RawMessage(tc.args))
		if got != tc.want {
			t.Errorf("SummarizeToolArgs(agent.await, %s) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

// Regression guard for the original defect: no valid argument shape may
// summarise to the empty string.
func TestSummarizeToolArgsAgentAwaitNeverEmpty(t *testing.T) {
	for _, args := range []string{`{"id":1}`, `{"all":true}`, `{"any":true}`} {
		if SummarizeToolArgs("agent.await", json.RawMessage(args)) == "" {
			t.Errorf("args %s summarised to empty", args)
		}
	}
}
