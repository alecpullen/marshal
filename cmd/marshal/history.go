package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"marshal/internal/db"
	"marshal/internal/history"
)

// runHistory implements `marshal history <session-id>`, the read-only view
// of a session's archived rollover generations. It opens the project
// database directly rather than starting a session, so an archive can be
// inspected without launching the TUI.
func runHistory(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	fs.SetOutput(stdout)
	generation := fs.Int("generation", -1, "dump this generation's full transcript")
	search := fs.String("search", "", "full-text search across archived turns")
	allSessions := fs.Bool("all-sessions", false, "with --search, search every session, not just this one")
	limit := fs.Int("limit", 25, "maximum search results")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New("marshal history: a session id is required (usage: marshal history <session-id> [--generation N] [--search QUERY])")
	}
	sessionID := rest[0]

	workingDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	database, err := db.Open(db.Path(workingDir))
	if err != nil {
		return fmt.Errorf("open project database: %w", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		return fmt.Errorf("migrate project database: %w", err)
	}

	var out string
	switch {
	case *search != "":
		scope := sessionID
		if *allSessions {
			scope = ""
		}
		out, err = history.Search(ctx, database, scope, *search, *limit)
	case *generation >= 0:
		out, err = history.DumpGeneration(ctx, database, history.DumpOptions{
			SessionID:     sessionID,
			GenerationSeq: *generation,
		})
	default:
		var summaries []history.GenerationSummary
		summaries, err = history.ListGenerations(ctx, database, sessionID)
		if err == nil {
			var b strings.Builder
			if len(summaries) == 0 {
				b.WriteString("No generations yet.")
			} else {
				b.WriteString("Generations:\n")
				for _, s := range summaries {
					status := "ended"
					if s.Live {
						status = "live"
					}
					fmt.Fprintf(&b, "  %d  %s  %d turns  digest=%s  %s\n",
						s.Seq, s.StartedAt.UTC().Format("2006-01-02 15:04:05"),
						s.TurnCount, s.SeedDigest, status)
				}
			}
			out = strings.TrimRight(b.String(), "\n")
		}
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, out)
	return nil
}
