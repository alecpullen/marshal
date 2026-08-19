package commands

import (
	"reflect"
	"testing"
)

func TestHistoryCommandHasNoNameMethod(t *testing.T) {
	c := &historyCommand{database: nil, sessionID: "test"}
	typ := reflect.TypeOf(c)
	if _, ok := typ.MethodByName("Name"); ok {
		t.Error("historyCommand should not have a Name() method — it's dead code")
	}
	if _, ok := typ.MethodByName("Description"); ok {
		t.Error("historyCommand should not have a Description() method — it's dead code")
	}
}
