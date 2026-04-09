package loop

import (
	"fmt"
	"sync"
)

// MockGitLayer simulates git operations for M2 testing.
// M3 will replace this with a real git implementation.
type MockGitLayer struct {
	mu            sync.Mutex
	currentBranch string
	commits       []string
	diffContent   string
	roundNum      int
}

// NewMockGitLayer creates a new mock git layer.
func NewMockGitLayer() *MockGitLayer {
	return &MockGitLayer{
		currentBranch: "main",
		commits:       make([]string, 0),
		diffContent:   "",
		roundNum:      0,
	}
}

// CreateIsolationBranch simulates creating a new branch.
func (m *MockGitLayer) CreateIsolationBranch(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	fmt.Printf("[mock git] Create isolation branch: %s\n", name)
	m.currentBranch = name
	return nil
}

// GetDiff returns a mock diff that changes each round.
func (m *MockGitLayer) GetDiff() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.roundNum++

	// First round: empty diff (no changes yet)
	if m.roundNum == 1 {
		return "", nil
	}

	// Subsequent rounds: return a mock diff
	m.diffContent = fmt.Sprintf(`diff --git a/example.go b/example.go
new file mode 100644
--- /dev/null
+++ b/example.go
@@ -0,0 +1,10 @@
+package main
+
+import "fmt"
+
+// Round %d implementation
+func main() {
+	fmt.Println("Hello from round %d")
+}
`, m.roundNum, m.roundNum)

	return m.diffContent, nil
}

// StageAndCommit simulates staging and committing changes.
func (m *MockGitLayer) StageAndCommit(message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	fmt.Printf("[mock git] Stage and commit: %s\n", message)
	m.commits = append(m.commits, message)
	return nil
}

// HardResetToHead simulates a hard reset to HEAD.
func (m *MockGitLayer) HardResetToHead() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	fmt.Printf("[mock git] Hard reset to HEAD\n")
	return nil
}

// DeleteBranch simulates deleting a branch.
func (m *MockGitLayer) DeleteBranch(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	fmt.Printf("[mock git] Delete branch: %s\n", name)
	if m.currentBranch == name {
		m.currentBranch = "main"
	}
	return nil
}

// CheckoutBranch simulates switching branches.
func (m *MockGitLayer) CheckoutBranch(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	fmt.Printf("[mock git] Checkout branch: %s\n", name)
	m.currentBranch = name
	return nil
}

// MergeBranch simulates merging a branch.
func (m *MockGitLayer) MergeBranch(name string, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	fmt.Printf("[mock git] Merge branch %s: %s\n", name, message)
	m.currentBranch = "main"
	return nil
}

// CurrentBranch returns the current branch name (for testing).
func (m *MockGitLayer) CurrentBranch() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.currentBranch
}

// Commits returns the list of commit messages (for testing).
func (m *MockGitLayer) Commits() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.commits...)
}
