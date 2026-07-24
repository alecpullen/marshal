package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRunDispatchesHistorySubcommand(t *testing.T) {
	original := historyRunner
	t.Cleanup(func() { historyRunner = original })

	var gotArgs []string
	sentinel := errors.New("history ran")
	historyRunner = func(_ context.Context, args []string, _ io.Writer) error {
		gotArgs = args
		return sentinel
	}

	err := run(context.Background(), []string{"history", "sess-1", "--generation", "2"},
		strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("run returned %v, want the history runner's error", err)
	}
	if strings.Join(gotArgs, " ") != "sess-1 --generation 2" {
		t.Fatalf("history args = %v, want the subcommand's own arguments", gotArgs)
	}
}

func TestRunHistoryRequiresASessionID(t *testing.T) {
	err := runHistory(context.Background(), nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("runHistory with no session id returned nil error, want a usage error")
	}
	if !strings.Contains(err.Error(), "session") {
		t.Fatalf("error = %v, want it to mention the missing session id", err)
	}
}
