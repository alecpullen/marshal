package main

import (
	"io"
	"testing"
)

func TestParseConfigDefaults(t *testing.T) {
	// Ensure env vars do not leak host state into the defaults test.
	for _, k := range []string{"WEBBRIDGE_ADDR", "WEBBRIDGE_TOKEN", "WEBBRIDGE_MARSHAL_BIN", "WEBBRIDGE_CWD_ROOT"} {
		t.Setenv(k, "")
	}
	cfg, err := parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.addr != "127.0.0.1:7700" {
		t.Errorf("addr = %q, want 127.0.0.1:7700", cfg.addr)
	}
	if cfg.token != "" {
		t.Errorf("token = %q, want empty (generated later)", cfg.token)
	}
	if cfg.marshalBin != "marshal" {
		t.Errorf("marshalBin = %q, want marshal", cfg.marshalBin)
	}
	if cfg.cwdRoot == "" {
		t.Error("cwdRoot empty; want fallback to working directory")
	}
}

func TestParseConfigEnvAndFlags(t *testing.T) {
	t.Setenv("WEBBRIDGE_ADDR", "127.0.0.1:9999")
	t.Setenv("WEBBRIDGE_TOKEN", "env-token")
	t.Setenv("WEBBRIDGE_CWD_ROOT", "/env/root")

	// Env applies when flags are absent.
	cfg, err := parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.addr != "127.0.0.1:9999" || cfg.token != "env-token" || cfg.cwdRoot != "/env/root" {
		t.Errorf("env not applied: %+v", cfg)
	}

	// Flags win over env.
	cfg, err = parseConfig([]string{
		"--addr", "127.0.0.1:1234",
		"--token", "flag-token",
		"--marshal-bin", "/bin/custom",
		"--cwd-root", "/flag/root",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	want := config{addr: "127.0.0.1:1234", token: "flag-token", marshalBin: "/bin/custom", cwdRoot: "/flag/root"}
	if cfg != want {
		t.Errorf("cfg = %+v, want %+v", cfg, want)
	}
}

func TestParseConfigRejectsArgs(t *testing.T) {
	if _, err := parseConfig([]string{"bogus"}, io.Discard); err == nil {
		t.Fatal("positional arg: expected error, got nil")
	}
	if _, err := parseConfig([]string{"--nope"}, io.Discard); err == nil {
		t.Fatal("unknown flag: expected error, got nil")
	}
}

func TestGenToken(t *testing.T) {
	tok, err := genToken()
	if err != nil {
		t.Fatalf("genToken: %v", err)
	}
	if len(tok) != 32 { // 16 bytes hex-encoded
		t.Errorf("token length = %d, want 32", len(tok))
	}
	tok2, err := genToken()
	if err != nil {
		t.Fatalf("genToken: %v", err)
	}
	if tok == tok2 {
		t.Error("two generated tokens are identical")
	}
}
