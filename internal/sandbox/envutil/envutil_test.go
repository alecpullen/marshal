package envutil

import "testing"

func TestSet(t *testing.T) {
	env := []string{"A=1", "B=2"}
	env = Set(env, "B", "9")
	if len(env) != 2 || env[1] != "B=9" {
		t.Fatalf("replace: %v", env)
	}
	env = Set(env, "C", "3")
	if len(env) != 3 || env[2] != "C=3" {
		t.Fatalf("append: %v", env)
	}
}

func TestFilterKeys(t *testing.T) {
	env := []string{"A=1", "SECRET=x", "B=2"}
	got := FilterKeys(env, map[string]bool{"SECRET": true})
	if len(got) != 2 || got[0] != "A=1" || got[1] != "B=2" {
		t.Fatalf("FilterKeys: %v", got)
	}
}
