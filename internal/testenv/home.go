package testenv

import (
	"fmt"
	"os"
	"testing"
)

var isolatedKeys = []string{
	"HOME", "USERPROFILE",
	"MARSHAL_CONFIG_DIR", "MARSHAL_DATA_DIR",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
	"OLLAMA_API_KEY", "OPENROUTER_API_KEY", "GROQ_API_KEY",
	"OPENAI_API_KEY", "OPENCODE_API_KEY", "ANTHROPIC_API_KEY",
	"GEMINI_API_KEY", "DEEPSEEK_API_KEY", "KIMI_API_KEY",
	"MISTRAL_API_KEY", "TOGETHER_API_KEY", "FIREWORKS_API_KEY",
	"XAI_API_KEY", "CEREBRAS_API_KEY",
}

// IsolateHome changes process environment; call before parallel work.
func IsolateHome() (func(), error) {
	dir, err := os.MkdirTemp("", "marshal-test-home-")
	if err != nil {
		return nil, err
	}
	type priorValue struct {
		value string
		set   bool
	}
	prior := make(map[string]priorValue, len(isolatedKeys))
	for _, key := range isolatedKeys {
		value, set := os.LookupEnv(key)
		prior[key] = priorValue{value: value, set: set}
	}
	cleanup := func() {
		for _, key := range isolatedKeys {
			previous := prior[key]
			if previous.set {
				_ = os.Setenv(key, previous.value)
			} else {
				_ = os.Unsetenv(key)
			}
		}
		_ = os.RemoveAll(dir)
	}
	for _, key := range isolatedKeys {
		value := ""
		if key == "HOME" || key == "USERPROFILE" {
			value = dir
		}
		if err := os.Setenv(key, value); err != nil {
			cleanup()
			return nil, err
		}
	}
	return cleanup, nil
}

// Run isolates package defaults without disabling runtime behavior.
func Run(m *testing.M) int {
	cleanup, err := IsolateHome()
	if err != nil {
		fmt.Fprintln(os.Stderr, "isolate test home:", err)
		return 1
	}
	defer cleanup()
	return m.Run()
}
