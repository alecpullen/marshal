package agent

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/alecpullen/marshal/internal/agent/tools"
	"github.com/alecpullen/marshal/internal/gateway"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ResultStatus indicates the outcome of agent execution.
type ResultStatus int

const (
	ResultStatusSuccess ResultStatus = iota
	ResultStatusError
	ResultStatusTimeout
	ResultStatusMaxIterations
	ResultStatusCancelled
	ResultStatusInvalidOutput // Output didn't match schema
)

// String returns the human-readable status.
func (s ResultStatus) String() string {
	switch s {
	case ResultStatusSuccess:
		return "success"
	case ResultStatusError:
		return "error"
	case ResultStatusTimeout:
		return "timeout"
	case ResultStatusMaxIterations:
		return "max_iterations"
	case ResultStatusCancelled:
		return "cancelled"
	case ResultStatusInvalidOutput:
		return "invalid_output"
	default:
		return "unknown"
	}
}

// Result contains the outcome of agent execution.
type Result struct {
	Status     ResultStatus
	Output     string
	Error      string
	Rounds     int
	ReadSet    []string      // Files that were read
	Usage      gateway.Usage // Token usage
	OutputJSON json.RawMessage // Structured output if schema was enforced

	// Sub-agent results (if any)
	SubAgentResults map[string]*SubAgentResult
}

// IsSuccess returns true if the result indicates successful completion.
func (r *Result) IsSuccess() bool {
	return r.Status == ResultStatusSuccess
}

// ValidateOutput checks if the output matches the manifest's output schema.
func (r *Result) ValidateOutput(schema json.RawMessage) error {
	if len(schema) == 0 {
		return nil // No schema to validate against
	}

	if !r.IsSuccess() {
		return fmt.Errorf("cannot validate output of failed execution")
	}

	// Parse schema into interface{}
	var schemaObj interface{}
	if err := json.Unmarshal(schema, &schemaObj); err != nil {
		return fmt.Errorf("invalid schema JSON: %w", err)
	}

	// Parse schema
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("schema.json", schemaObj); err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}

	sch, err := compiler.Compile("schema.json")
	if err != nil {
		return fmt.Errorf("compile schema: %w", err)
	}

	// Parse output as JSON
	var output interface{}
	if err := json.Unmarshal([]byte(r.Output), &output); err != nil {
		// Try to extract JSON from markdown code blocks
		output = extractJSONFromMarkdown(r.Output)
		if output == nil {
			return fmt.Errorf("output is not valid JSON: %w", err)
		}
	}

	// Validate
	if err := sch.Validate(output); err != nil {
		return fmt.Errorf("output validation failed: %w", err)
	}

	// Store validated output
	outputBytes, _ := json.Marshal(output)
	r.OutputJSON = outputBytes

	return nil
}

// extractJSONFromMarkdown extracts JSON from markdown code blocks.
func extractJSONFromMarkdown(text string) interface{} {
	// Pattern: ```json\n{...}\n```
	jsonBlockRegex := regexp.MustCompile("```(?:json)?\\s*\\n?([{\\[][\\s\\S]*?[}\\]])\\s*\\n?```")
	matches := jsonBlockRegex.FindStringSubmatch(text)
	
	if len(matches) > 1 {
		var output interface{}
		if err := json.Unmarshal([]byte(matches[1]), &output); err == nil {
			return output
		}
	}

	// Try finding raw JSON object at end of text
	rawJSONRegex := regexp.MustCompile(`[{\[][\s\S]*?[}\]]\s*$`)
	if match := rawJSONRegex.FindString(text); match != "" {
		var output interface{}
		if err := json.Unmarshal([]byte(match), &output); err == nil {
			return output
		}
	}

	return nil
}

// ReadBeforeEditError indicates a file was modified without being read first.
type ReadBeforeEditError struct {
	Path string
	Hint string
}

func (e *ReadBeforeEditError) Error() string {
	return fmt.Sprintf("read-before-edit violation: %s (%s)", e.Path, e.Hint)
}

// IsRetryable checks if an error is retryable by the agent.
func IsRetryable(err error) bool {
	if toolErr, ok := err.(*tools.ToolError); ok {
		return toolErr.IsRetryableError()
	}
	return false
}

// IsCritical checks if an error is critical and should surface to orchestrator.
func IsCritical(err error) bool {
	if toolErr, ok := err.(*tools.ToolError); ok {
		return toolErr.IsCriticalError()
	}
	return true // Unknown errors are treated as critical
}

// Helper functions for result creation
func calculateTotalUsage(rounds []Round) gateway.Usage {
	var total gateway.Usage
	for _, r := range rounds {
		total.InputTokens += r.Usage.InputTokens
		total.OutputTokens += r.Usage.OutputTokens
		total.TotalTokens += r.Usage.TotalTokens
	}
	return total
}

// truncate truncates a string to n characters.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
