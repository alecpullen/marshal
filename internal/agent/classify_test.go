package agent

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		goal string
		want TaskClass
	}{
		{"plain question", "What does this project do?", ClassQuestion},
		{"fix implies edit", "Fix the failing parser test", ClassEdit},
		{"add implies edit", "Add a small test for the diff engine", ClassEdit},
		{"refactor implies edit", "Refactor the session package", ClassEdit},
		{"run implies command", "Run the test suite", ClassCommand},
		{"build implies command", "Build the binary and check for errors", ClassCommand},
		{"edit keyword wins over command keyword", "Fix the tests that run too slowly", ClassEdit},
		{"runtime does not match run", "Debug the runtime error in the scheduler", ClassQuestion},
		{"running does not match run", "The process is running fine", ClassQuestion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.goal)
			if got != tt.want {
				t.Fatalf("Classify(%q) = %q, want %q", tt.goal, got, tt.want)
			}
		})
	}
}
