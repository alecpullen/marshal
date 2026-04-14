// Package pipeline provides sequential and parallel pipeline execution for Marshal.
package pipeline

import (
	"fmt"
)

// ProgressCallback is called to report pipeline execution progress.
type ProgressCallback func(event ProgressEvent)

// ProgressEvent represents a progress update during pipeline execution.
type ProgressEvent struct {
	Type     string // "task_start", "task_complete", "task_failed", "merge", "complete"
	TaskID   string
	Status   string
	Message  string
	Progress float64 // 0.0 to 1.0
}

// ConsoleProgressHandler is a default progress handler that prints to console.
func ConsoleProgressHandler(event ProgressEvent) {
	switch event.Type {
	case "task_start":
		fmt.Printf("[→] Task %s: %s\n", event.TaskID, event.Message)
	case "task_complete":
		fmt.Printf("[✓] Task %s: %s\n", event.TaskID, event.Message)
	case "task_failed":
		fmt.Printf("[✗] Task %s: %s\n", event.TaskID, event.Message)
	case "merge":
		fmt.Printf("[⇒] %s\n", event.Message)
	case "complete":
		fmt.Printf("[★] %s\n", event.Message)
	}
}

// NoopProgressHandler is a progress handler that does nothing (for silent execution).
func NoopProgressHandler(event ProgressEvent) {}
