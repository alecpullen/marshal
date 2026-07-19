package db

import "testing"

func TestPath(t *testing.T) {
	if got := Path("/repo"); got != "/repo/.marshal/marshal.db" {
		t.Errorf("Path = %q", got)
	}
}
