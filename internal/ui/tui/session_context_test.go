package tui

import (
	"testing"
)

func TestLoadSessionContext_NilRepo(t *testing.T) {
	result := loadSessionContext(nil)
	if result != "" {
		t.Errorf("expected empty result for nil repo, got: %s", result)
	}
}
