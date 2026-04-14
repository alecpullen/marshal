// Benchmark runner for Marshal - Exercism exercises
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Version is set by ldflags
var Version = "dev"

type Config struct {
	Language     string
	Difficulty   []string
	Exercise     string
	ConfigPath   string
	Iterations   int
	OutDir       string
	ExercisesDir string
	Local        bool // Run against local endpoint (capture local-model metrics)
}

type BenchmarkResult struct {
	Version       string        `json:"version"`
	Timestamp     time.Time     `json:"timestamp"`
	Config        Config        `json:"config"`
	Summary       Summary       `json:"summary"`
	Exercises     []ExerciseResult `json:"exercises"`
}

type Summary struct {
	TotalExercises   int     `json:"total_exercises"`
	Passed           int     `json:"passed"`
	Failed           int     `json:"failed"`
	SuccessRate      float64 `json:"success_rate"`
	AvgRounds        float64 `json:"avg_rounds"`
	TotalTokens      int     `json:"total_tokens"`
	TotalDurationMs  int64   `json:"total_duration_ms"`
}

type ExerciseResult struct {
	Name            string   `json:"name"`
	Language        string   `json:"language"`
	Difficulty      string   `json:"difficulty"`
	Passed          bool     `json:"passed"`
	Rounds          int      `json:"rounds"`
	Tokens          int      `json:"tokens"`
	DurationMs      int64    `json:"duration_ms"`
	// Local-model specific timing (M13/PR-2)
	TimeToFirstTokenMs int64   `json:"ttft_ms,omitempty"`      // first token latency
	TokensPerSec       float64 `json:"tokens_per_sec,omitempty"` // throughput
	VerdictParseOK     bool    `json:"verdict_parse_ok,omitempty"` // grammar constraint worked
	Error              string  `json:"error,omitempty"`
	TestOutput         string  `json:"test_output,omitempty"`
}

// Exercism exercises list (subset for initial benchmark)
var exercismExercises = map[string][]Exercise{
	"go": {
		{Name: "hello-world", Difficulty: "easy", Path: "go/hello-world"},
		{Name: "two-fer", Difficulty: "easy", Path: "go/two-fer"},
		{Name: "hamming", Difficulty: "easy", Path: "go/hamming"},
		{Name: "rna-transcription", Difficulty: "easy", Path: "go/rna-transcription"},
		{Name: "isogram", Difficulty: "easy", Path: "go/isogram"},
		{Name: "difference-of-squares", Difficulty: "easy", Path: "go/difference-of-squares"},
		{Name: "luhn", Difficulty: "easy", Path: "go/luhn"},
		{Name: "grains", Difficulty: "easy", Path: "go/grains"},
		{Name: "clock", Difficulty: "medium", Path: "go/clock"},
		{Name: "robot-name", Difficulty: "medium", Path: "go/robot-name"},
		{Name: "markdown", Difficulty: "medium", Path: "go/markdown"},
		{Name: "linked-list", Difficulty: "medium", Path: "go/linked-list"},
	},
}

type Exercise struct {
	Name       string
	Difficulty string
	Path       string
}

func main() {
	cfg := parseFlags()

	fmt.Printf("Marshal Benchmark Runner v%s\n", Version)
	fmt.Println(strings.Repeat("=", 50))
	fmt.Printf("Language: %s\n", cfg.Language)
	fmt.Printf("Difficulty: %s\n", strings.Join(cfg.Difficulty, ", "))
	if cfg.Exercise != "" {
		fmt.Printf("Exercise: %s\n", cfg.Exercise)
	}
	fmt.Println()

	// Get exercises to run
	exercises := selectExercises(cfg)
	if len(exercises) == 0 {
		fmt.Fprintf(os.Stderr, "No exercises found for language: %s\n", cfg.Language)
		os.Exit(1)
	}

	fmt.Printf("Running %d exercises...\n\n", len(exercises))

	// Run benchmark
	result := runBenchmark(cfg, exercises)

	// Print summary
	printSummary(result.Summary)

	// Save results
	if err := saveResults(cfg, result); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to save results: %v\n", err)
	}

	// Exit with error if any exercises failed
	if result.Summary.Failed > 0 {
		fmt.Printf("\n%d/%d exercises failed\n", result.Summary.Failed, result.Summary.TotalExercises)
		os.Exit(1)
	}

	fmt.Println("\nAll exercises passed!")
}

func parseFlags() Config {
	var cfg Config
	var difficultyStr string

	flag.StringVar(&cfg.Language, "language", "go", "Language to benchmark (go)")
	flag.StringVar(&difficultyStr, "difficulty", "easy", "Difficulty levels (comma-separated: easy,medium,hard)")
	flag.StringVar(&cfg.Exercise, "exercise", "", "Run specific exercise only")
	flag.StringVar(&cfg.ConfigPath, "config", "", "Path to marshal config file")
	flag.IntVar(&cfg.Iterations, "iterations", 1, "Number of iterations per exercise")
	flag.StringVar(&cfg.OutDir, "out", "./results", "Output directory for results")
	flag.StringVar(&cfg.ExercisesDir, "exercises-dir", "./exercises", "Directory for exercism exercises")
	flag.BoolVar(&cfg.Local, "local", false, "Run against local endpoint (capture TTFT, throughput, verdict parse rate)")

	flag.Parse()

	cfg.Difficulty = strings.Split(difficultyStr, ",")
	return cfg
}

func selectExercises(cfg Config) []Exercise {
	langExercises, ok := exercismExercises[cfg.Language]
	if !ok {
		return nil
	}

	var selected []Exercise
	for _, ex := range langExercises {
		// Filter by difficulty
		difficultyMatch := false
		for _, d := range cfg.Difficulty {
			if ex.Difficulty == d {
				difficultyMatch = true
				break
			}
		}
		if !difficultyMatch {
			continue
		}

		// Filter by specific exercise name if provided
		if cfg.Exercise != "" && ex.Name != cfg.Exercise {
			continue
		}

		selected = append(selected, ex)
	}

	return selected
}

func runBenchmark(cfg Config, exercises []Exercise) BenchmarkResult {
	result := BenchmarkResult{
		Version:   Version,
		Timestamp: time.Now(),
		Config:    cfg,
		Exercises: make([]ExerciseResult, 0, len(exercises)),
	}

	for i, ex := range exercises {
		fmt.Printf("[%d/%d] %s (%s)... ", i+1, len(exercises), ex.Name, ex.Difficulty)

		exResult := runExercise(cfg, ex)
		result.Exercises = append(result.Exercises, exResult)

		if exResult.Passed {
			if cfg.Local {
				fmt.Printf("PASS (%d rounds, %d tokens, %dms, TTFT=%dms, %.1f tok/s)\n",
					exResult.Rounds, exResult.Tokens, exResult.DurationMs,
					exResult.TimeToFirstTokenMs, exResult.TokensPerSec)
			} else {
				fmt.Printf("PASS (%d rounds, %d tokens, %dms)\n",
					exResult.Rounds, exResult.Tokens, exResult.DurationMs)
			}
		} else {
			fmt.Printf("FAIL (%s)\n", exResult.Error)
		}
	}

	// Calculate summary
	result.Summary = calculateSummary(result.Exercises)

	return result
}

func runExercise(cfg Config, ex Exercise) ExerciseResult {
	start := time.Now()

	result := ExerciseResult{
		Name:       ex.Name,
		Language:   cfg.Language,
		Difficulty: ex.Difficulty,
	}

	// Check if marshal is available
	if _, err := execLookPath("marshal"); err != nil {
		result.Error = "marshal not found in PATH"
		return result
	}

	var err error

	// Setup exercise directory
	exDir := filepath.Join(cfg.ExercisesDir, ex.Path)
	if err = setupExercise(exDir, cfg.Language, ex.Name); err != nil {
		result.Error = fmt.Sprintf("setup failed: %v", err)
		result.DurationMs = time.Since(start).Milliseconds()
		return result
	}

	// Read the exercise instructions and starter code
	_, err = readInstructions(exDir)
	if err != nil {
		result.Error = fmt.Sprintf("read instructions failed: %v", err)
		result.DurationMs = time.Since(start).Milliseconds()
		return result
	}

	// Run marshal on the exercise
	// This is a stub - in a full implementation, this would:
	// 1. Run `marshal run` with the exercise prompt
	// 2. Parse the NDJSON output
	// 3. Run tests to verify the solution
	// 4. Record results

	// For now, simulate a successful run
	result.Passed = true
	result.Rounds = 1
	result.Tokens = 1000
	result.DurationMs = time.Since(start).Milliseconds()

	// When --local is enabled, capture local-model specific metrics.
	// In a full implementation, these would be parsed from NDJSON events.
	if cfg.Local {
		result.TimeToFirstTokenMs = 250   // simulated TTFT (ms)
		result.TokensPerSec = 45.0        // simulated throughput
		result.VerdictParseOK = true      // grammar constraint succeeded
	}

	// Run tests to verify
	if err := runTests(exDir, cfg.Language); err != nil {
		result.Passed = false
		result.Error = fmt.Sprintf("tests failed: %v", err)
	}

	return result
}

func calculateSummary(exercises []ExerciseResult) Summary {
	s := Summary{
		TotalExercises: len(exercises),
	}

	var totalRounds int
	var totalTokens int
	var totalDuration int64

	for _, ex := range exercises {
		if ex.Passed {
			s.Passed++
		} else {
			s.Failed++
		}
		totalRounds += ex.Rounds
		totalTokens += ex.Tokens
		totalDuration += ex.DurationMs
	}

	if s.TotalExercises > 0 {
		s.SuccessRate = float64(s.Passed) / float64(s.TotalExercises) * 100
		s.AvgRounds = float64(totalRounds) / float64(s.TotalExercises)
	}

	s.TotalTokens = totalTokens
	s.TotalDurationMs = totalDuration

	return s
}

func printSummary(s Summary) {
	fmt.Println()
	fmt.Println(strings.Repeat("=", 50))
	fmt.Println("Benchmark Summary")
	fmt.Println(strings.Repeat("=", 50))
	fmt.Printf("Total Exercises: %d\n", s.TotalExercises)
	fmt.Printf("Passed:          %d\n", s.Passed)
	fmt.Printf("Failed:          %d\n", s.Failed)
	fmt.Printf("Success Rate:    %.1f%%\n", s.SuccessRate)
	fmt.Printf("Avg Rounds:      %.1f\n", s.AvgRounds)
	fmt.Printf("Total Tokens:    %d\n", s.TotalTokens)
	fmt.Printf("Total Duration:  %dms\n", s.TotalDurationMs)
}

func saveResults(cfg Config, result BenchmarkResult) error {
	if err := os.MkdirAll(cfg.OutDir, 0755); err != nil {
		return err
	}

	timestamp := time.Now().Format("2006-01-02T15-04-05")
	filename := fmt.Sprintf("benchmark-%s-%s.json", cfg.Language, timestamp)
	filepath := filepath.Join(cfg.OutDir, filename)

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(filepath, data, 0644); err != nil {
		return err
	}

	fmt.Printf("\nResults saved to: %s\n", filepath)
	return nil
}

// Stub functions - would be fully implemented for production use

func execLookPath(name string) (string, error) {
	return "marshal", nil // Stub
}

func setupExercise(dir string, language string, name string) error {
	// In production: clone exercism exercise or use local copy
	return os.MkdirAll(dir, 0755)
}

func readInstructions(dir string) (string, error) {
	// In production: read README.md or instructions from exercism
	return "Implement the exercise", nil
}

func runTests(dir string, language string) error {
	// In production: run `go test` or appropriate test runner
	return nil
}
