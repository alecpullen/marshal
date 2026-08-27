package native

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"marshal/internal/tools/registry"
)

const (
	defaultCSVInspectLimit = 5
	hardCSVInspectLimit    = 200
	// maxCSVScanRows bounds the scan for total_rows / where matching so a
	// hostile multi-GB CSV can't stall the agent loop.
	maxCSVScanRows = 1000000
)

type csvInspectArgs struct {
	Path      string   `json:"path"`
	Columns   []string `json:"columns"`
	Where     string   `json:"where"`
	Offset    int      `json:"offset"`
	Limit     int      `json:"limit"`
	Delimiter string   `json:"delimiter"`
}

func (t *toolSet) csvInspectTool() registry.Tool {
	tool := registry.Tool{
		Name:        "csv.inspect",
		Description: "Inspect a workspace CSV file: column names, total row count, and sample rows. Optionally project a subset of columns, filter rows with a single column=value equality, and page with offset/limit. Delimiter is auto-detected (comma, tab, semicolon, pipe). Prefer this over shelling out to python or awk.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"columns":{"type":"array","items":{"type":"string"}},"where":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"},"delimiter":{"type":"string"}},"required":["path"],"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[csvInspectArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if args.Path == "" {
			return registry.ToolResult{}, fmt.Errorf("csv.inspect path is required")
		}
		if args.Offset < 0 {
			return registry.ToolResult{}, fmt.Errorf("csv.inspect offset must be >= 0, got %d", args.Offset)
		}

		var whereCol, whereVal string
		hasWhere := false
		if args.Where != "" {
			col, val, ok := strings.Cut(args.Where, "=")
			if !ok || col == "" {
				return registry.ToolResult{}, fmt.Errorf("csv.inspect where must be column=value, got %q", args.Where)
			}
			whereCol, whereVal, hasWhere = col, val, true
		}

		delim, err := parseDelimiter(args.Delimiter)
		if err != nil {
			return registry.ToolResult{}, err
		}

		path, err := resolveNamedRoot(t.namedRoots, t.activeRoot(), t.effectiveAdditionalRoots(), args.Path)
		if err != nil {
			return registry.ToolResult{}, err
		}
		f, err := os.Open(path)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("open %s: %w", args.Path, err)
		}
		defer f.Close()

		br := bufio.NewReader(f)
		if delim == 0 {
			head, err := br.ReadString('\n')
			if err != nil && head == "" {
				return registry.ToolResult{}, fmt.Errorf("%s is empty", args.Path)
			}
			delim = detectDelimiter(head)
			br = bufio.NewReader(io.MultiReader(strings.NewReader(head), br))
		}

		r := csv.NewReader(br)
		r.Comma = delim
		r.FieldsPerRecord = -1 // tolerate ragged rows in an inspection tool
		r.LazyQuotes = true

		header, err := r.Read()
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("read %s header: %w", args.Path, err)
		}
		colIndex := make(map[string]int, len(header))
		for i, name := range header {
			colIndex[name] = i
		}

		project := make([]int, 0, len(header))
		if len(args.Columns) == 0 {
			for i := range header {
				project = append(project, i)
			}
		} else {
			for _, name := range args.Columns {
				i, ok := colIndex[name]
				if !ok {
					return registry.ToolResult{}, fmt.Errorf("csv.inspect unknown column %q; available: %s", name, strings.Join(header, ", "))
				}
				project = append(project, i)
			}
		}
		whereIdx := -1
		if hasWhere {
			i, ok := colIndex[whereCol]
			if !ok {
				return registry.ToolResult{}, fmt.Errorf("csv.inspect unknown where column %q; available: %s", whereCol, strings.Join(header, ", "))
			}
			whereIdx = i
		}

		limit := args.Limit
		if limit <= 0 {
			limit = defaultCSVInspectLimit
		}
		if limit > hardCSVInspectLimit {
			limit = hardCSVInspectLimit
		}

		field := func(row []string, i int) string {
			if i < len(row) {
				return row[i]
			}
			return ""
		}

		totalRows := 0
		matchedRows := 0
		var sample [][]string
		scanCapped := false
		for {
			row, err := r.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("parse %s near row %d: %w", args.Path, totalRows+2, err)
			}
			if err := ctx.Err(); err != nil {
				return registry.ToolResult{}, err
			}
			totalRows++
			if totalRows > maxCSVScanRows {
				scanCapped = true
				totalRows--
				break
			}
			if whereIdx >= 0 && field(row, whereIdx) != whereVal {
				continue
			}
			if matchedRows >= args.Offset && len(sample) < limit {
				out := make([]string, len(project))
				for j, i := range project {
					out[j] = field(row, i)
				}
				sample = append(sample, out)
			}
			matchedRows++
		}

		var body bytes.Buffer
		w := csv.NewWriter(&body)
		headerOut := make([]string, len(project))
		for j, i := range project {
			headerOut[j] = header[i]
		}
		_ = w.Write(headerOut)
		for _, row := range sample {
			_ = w.Write(row)
		}
		w.Flush()

		var head strings.Builder
		fmt.Fprintf(&head, "columns: %s\ntotal_rows: %d", strings.Join(header, ", "), totalRows)
		if scanCapped {
			fmt.Fprintf(&head, " (scan capped at %d rows)", maxCSVScanRows)
		}
		if hasWhere {
			fmt.Fprintf(&head, "\nwhere %s=%s matched %d rows", whereCol, whereVal, matchedRows)
		}
		fmt.Fprintf(&head, "\nshowing %d row(s) from offset %d\n\n", len(sample), args.Offset)

		summary := fmt.Sprintf("%d rows · %d columns", totalRows, len(header))
		if hasWhere {
			summary = fmt.Sprintf("%d/%d rows matched · %d columns", matchedRows, totalRows, len(header))
		}
		return registry.ToolResult{
			Summary: summary,
			Content: limitOutput(head.String()+body.String(), t.maxOutputBytes),
		}, nil
	}
	return tool
}

// parseDelimiter converts the optional delimiter argument to a rune. It
// returns 0 when no delimiter was given, signalling auto-detection.
func parseDelimiter(s string) (rune, error) {
	switch s {
	case "":
		return 0, nil
	case "comma", ",":
		return ',', nil
	case "tab", "\t":
		return '\t', nil
	case "semicolon", ";":
		return ';', nil
	case "pipe", "|":
		return '|', nil
	default:
		return 0, fmt.Errorf("csv.inspect delimiter must be one of comma|tab|semicolon|pipe (or the literal character), got %q", s)
	}
}

// detectDelimiter picks the most frequent candidate delimiter in the header
// line. Comma wins ties and is the fallback when no candidate appears.
func detectDelimiter(head string) rune {
	best := ','
	bestCount := -1
	for _, c := range []rune{',', '\t', ';', '|'} {
		if n := strings.Count(head, string(c)); n > bestCount {
			best, bestCount = c, n
		}
	}
	return best
}
