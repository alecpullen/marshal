package app

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/logging"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/index"
	"marshal/internal/pubsub"
	"marshal/internal/worker"
)

func TestBuildIndexWorkersStartupScanPublishesReport(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	projectID, err := database.GetOrCreateProject(dir, "startup-index-test")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	cfg := config.Default()
	state := session.New(cfg, dir, time.Unix(100, 0), session.Persistence{})
	logger := logging.New(io.Discard, slog.LevelInfo, false)
	broker := pubsub.NewBroker[index.Report]()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reports := broker.Subscribe(ctx)

	workers := buildIndexWorkers(cfg, state, database, projectID, dir, nil, broker, logger)
	var startup worker.Worker
	for _, w := range workers {
		if w.Name() == "startup-index-scan" {
			startup = w
		}
	}
	if startup == nil {
		t.Fatalf("startup-index-scan worker missing, got %d workers", len(workers))
	}
	if err := startup.Run(ctx); err != nil {
		t.Fatalf("startup scan: %v", err)
	}

	select {
	case ev := <-reports:
		if ev.Type != indexEventCompleted {
			t.Fatalf("event type = %q, want %q", ev.Type, indexEventCompleted)
		}
		if ev.Payload.Files == 0 {
			t.Fatal("report shows no files indexed")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no index report published")
	}

	files, err := database.GetFileIndex(projectID, 0)
	if err != nil || len(files) == 0 {
		t.Fatalf("file index empty after startup scan: %v", err)
	}
}

func TestBuildIndexWorkersWatcherStillGated(t *testing.T) {
	cfg := config.Default() // watch unset => watcher off
	workers := buildIndexWorkers(cfg, session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{}),
		nil, 0, t.TempDir(), nil, pubsub.NewBroker[index.Report](), logging.New(io.Discard, slog.LevelInfo, false))
	for _, w := range workers {
		if w.Name() == "index-watcher" {
			t.Fatal("watcher must stay gated on config.WatchEnabled")
		}
	}
}

func TestSubscribeIndexReseedSeedsOnReport(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	projectID, err := database.GetOrCreateProject(dir, "reseed-test")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := database.SaveFileIndex(projectID, []db.FileIndex{{Path: "main.go", Language: "go"}}); err != nil {
		t.Fatalf("save file index: %v", err)
	}

	cfg := config.Default()
	cfg.Project.Name = "reseed-test"
	state := session.New(cfg, dir, time.Unix(100, 0), session.Persistence{})
	workCtx, workCancel := context.WithCancel(context.Background())
	defer workCancel()
	rt := &Runtime{State: state, workCtx: workCtx}
	broker := pubsub.NewBroker[index.Report]()
	rt.subscribeIndexReseed(broker, database, projectID)

	if !state.ContextPack().IsEmpty() {
		t.Fatal("pack should start empty before any report")
	}
	broker.Publish(indexEventCompleted, index.Report{Files: 1})

	deadline := time.Now().Add(5 * time.Second)
	for state.ContextPack().IsEmpty() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if state.ContextPack().IsEmpty() {
		t.Fatal("pack was not re-seeded after index report")
	}
}
