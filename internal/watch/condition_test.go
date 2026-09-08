package watch

import (
	"strings"
	"testing"
)

func TestParseCondition(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "empty defaults to change", raw: ""},
		{name: "change", raw: "change"},
		{name: "change with args rejected", raw: "change foo", wantErr: true},
		{name: "exit_code", raw: "exit_code 0"},
		{name: "exit_code non-integer rejected", raw: "exit_code abc", wantErr: true},
		{name: "exit_code missing arg rejected", raw: "exit_code", wantErr: true},
		{name: "regex", raw: "regex foo.*bar"},
		{name: "regex missing pattern rejected", raw: "regex", wantErr: true},
		{name: "regex invalid pattern rejected", raw: "regex [", wantErr: true},
		{name: "json", raw: "json a.b[0].c = 5"},
		{name: "json missing fields rejected", raw: "json a.b", wantErr: true},
		{name: "json bad op rejected", raw: "json a.b == 5", wantErr: true},
		{name: "unknown type rejected", raw: "bogus x", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseCondition(tt.raw)
			if tt.wantErr != (err != nil) {
				t.Fatalf("parseCondition(%q) err = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
		})
	}
}

func TestChangeConditionEval(t *testing.T) {
	c, err := parseCondition("change")
	if err != nil {
		t.Fatal(err)
	}
	base := Sample{Stdout: "hello", ExitCode: 0}
	if c.Eval(base, base) {
		t.Error("change should not fire on identical samples")
	}
	if !c.Eval(Sample{Stdout: "world", ExitCode: 0}, base) {
		t.Error("change should fire on different stdout")
	}
	if !c.Eval(Sample{Stdout: "hello", ExitCode: 1}, base) {
		t.Error("change should fire on different exit code")
	}
}

func TestExitCodeConditionEval(t *testing.T) {
	c, err := parseCondition("exit_code 0")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Eval(Sample{ExitCode: 0}, Sample{}) {
		t.Error("exit_code 0 should fire on exit 0")
	}
	if c.Eval(Sample{ExitCode: 1}, Sample{}) {
		t.Error("exit_code 0 should not fire on exit 1")
	}
}

func TestRegexConditionEval(t *testing.T) {
	c, err := parseCondition("regex foo.*bar")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Eval(Sample{Stdout: "foo123bar"}, Sample{}) {
		t.Error("regex should match foo123bar")
	}
	if c.Eval(Sample{Stdout: "baz"}, Sample{}) {
		t.Error("regex should not match baz")
	}
}

func TestJSONConditionEval(t *testing.T) {
	tests := []struct {
		name   string
		cond   string
		sample Sample
		want   bool
	}{
		{name: "eq numeric", cond: "json a.b = 5", sample: Sample{Stdout: `{"a":{"b":5}}`}, want: true},
		{name: "eq numeric mismatch", cond: "json a.b = 6", sample: Sample{Stdout: `{"a":{"b":5}}`}, want: false},
		{name: "neq", cond: "json a.b != 6", sample: Sample{Stdout: `{"a":{"b":5}}`}, want: true},
		{name: "lt", cond: "json a.b < 10", sample: Sample{Stdout: `{"a":{"b":5}}`}, want: true},
		{name: "gt", cond: "json a.b > 10", sample: Sample{Stdout: `{"a":{"b":5}}`}, want: false},
		{name: "string eq", cond: "json name = alice", sample: Sample{Stdout: `{"name":"alice"}`}, want: true},
		{name: "array index", cond: "json items[0] = 1", sample: Sample{Stdout: `{"items":[1,2,3]}`}, want: true},
		{name: "nested array index", cond: "json a.b[1].c = x", sample: Sample{Stdout: `{"a":{"b":[{"c":"y"},{"c":"x"}]}}`}, want: true},
		{name: "missing path", cond: "json a.b = 5", sample: Sample{Stdout: `{"a":{}}`}, want: false},
		{name: "non-json sample", cond: "json a.b = 5", sample: Sample{Stdout: "not json"}, want: false},
		{name: "empty sample", cond: "json a.b = 5", sample: Sample{}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := parseCondition(tt.cond)
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Eval(tt.sample, Sample{}); got != tt.want {
				t.Errorf("Eval(%q) = %v, want %v", tt.cond, got, tt.want)
			}
		})
	}
}

func TestTailCap(t *testing.T) {
	short := strings.Repeat("a", 100)
	if got := tailCap(short); got != short {
		t.Error("short string should be unchanged")
	}
	long := strings.Repeat("a", SampleTailCap+100)
	got := tailCap(long)
	if len(got) != SampleTailCap {
		t.Fatalf("tailCap length = %d, want %d", len(got), SampleTailCap)
	}
	if !strings.HasSuffix(long, got) {
		t.Error("tailCap should keep the tail")
	}
}
