package settings

import (
	"testing"
)

func TestHumanizedByteFields(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
		{1099511627776, "1.0 TB"},
	}

	for _, tt := range tests {
		got := formatBytes(tt.input)
		if got != tt.expected {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.input, got, tt.expected)
		}
		// Round-trip: parse the formatted string back
		parsed, err := parseBytes(got)
		if err != nil {
			t.Errorf("parseBytes(%q) error: %v", got, err)
		}
		// Re-format the parsed value — it should match (within rounding)
		reformatted := formatBytes(parsed)
		if reformatted != got {
			t.Logf("round-trip: formatBytes(%d)=%q → parseBytes=%d → formatBytes=%q", tt.input, got, parsed, reformatted)
		}
	}
}

func TestParseBytes(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		wantErr  bool
	}{
		{"0", 0, false},
		{"1024", 1024, false},
		{"1 KB", 1024, false},
		{"1.5 KB", 1536, false},
		{"1 MB", 1048576, false},
		{"1 GB", 1073741824, false},
		{"1 TB", 1099511627776, false},
		{"", 0, true},
		{"xyz", 0, true},
		{"1 ZB", 0, true},
	}

	for _, tt := range tests {
		got, err := parseBytes(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseBytes(%q) expected error", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseBytes(%q) unexpected error: %v", tt.input, err)
			continue
		}
		if got != tt.expected {
			t.Errorf("parseBytes(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestBytesFieldRoundTrip(t *testing.T) {
	var val int64 = 1048576 // 1 MB
	f := bytesField("test.bytes", "Test Bytes",
		func() int64 { return val },
		func(v int64) { val = v },
	)
	if f.GetStr() != "1.0 MB" {
		t.Fatalf("expected 1.0 MB, got %q", f.GetStr())
	}
	if err := f.SetStr("2 MB"); err != nil {
		t.Fatalf("setStr error: %v", err)
	}
	if val != 2097152 {
		t.Fatalf("expected 2097152, got %d", val)
	}
	if f.GetStr() != "2.0 MB" {
		t.Fatalf("expected 2.0 MB, got %q", f.GetStr())
	}
}
