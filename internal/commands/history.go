package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"marshal/internal/db"
	"marshal/internal/history"
)

// historyCommand implements the /history slash command.
type historyCommand struct {
	database  *db.DB
	sessionID string
}

// Run executes the /history command with the given args.
// Subcommands:
//
//	/history                  — list all generations
//	/history dump <seq>       — dump a generation's transcript
//	/history search <query>   — search archived turns
func (c *historyCommand) Run(ctx context.Context, args []string) (string, error) {
	if len(args) == 0 {
		return c.listGenerations(ctx)
	}

	switch args[0] {
	case "dump":
		return c.dumpGeneration(ctx, args[1:])
	case "search":
		return c.search(ctx, args[1:])
	default:
		return "", fmt.Errorf("unknown subcommand %q; expected dump or search", args[0])
	}
}

func (c *historyCommand) listGenerations(ctx context.Context) (string, error) {
	summaries, err := history.ListGenerations(ctx, c.database, c.sessionID)
	if err != nil {
		return "", fmt.Errorf("list generations: %w", err)
	}
	if len(summaries) == 0 {
		return "No generations yet.", nil
	}
	var b strings.Builder
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
	return strings.TrimRight(b.String(), "\n"), nil
}

func (c *historyCommand) dumpGeneration(ctx context.Context, args []string) (string, error) {
	if len(args) < 1 {
		return "", fmt.Errorf("usage: /history dump <seq>")
	}
	seq, err := strconv.Atoi(args[0])
	if err != nil {
		return "", fmt.Errorf("invalid sequence number %q", args[0])
	}
	out, err := history.DumpGeneration(ctx, c.database, history.DumpOptions{
		SessionID:     c.sessionID,
		GenerationSeq: seq,
	})
	if err != nil {
		return "", fmt.Errorf("dump generation: %w", err)
	}
	return out, nil
}

func (c *historyCommand) search(ctx context.Context, args []string) (string, error) {
	if len(args) < 1 {
		return "", fmt.Errorf("usage: /history search <query>")
	}
	query := strings.Join(args, " ")
	out, err := history.Search(ctx, c.database, c.sessionID, query, 20)
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}
	return out, nil
}
