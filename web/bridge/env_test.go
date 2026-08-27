package bridge

import (
	"strings"
	"testing"
)

func TestClientEnvCarriesRuntimeVarsNotSecrets(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://10.0.0.5:2376")
	t.Setenv("HTTPS_PROXY", "http://proxy:8080")
	t.Setenv("ANTHROPIC_API_KEY", "sk-must-not-leak")

	env := clientEnv()
	joined := strings.Join(env, " ")

	if !strings.Contains(joined, "DOCKER_HOST=tcp://10.0.0.5:2376") {
		t.Errorf("DOCKER_HOST missing; docker cannot reach a remote daemon\ngot: %s", joined)
	}
	if !strings.Contains(joined, "HTTPS_PROXY=http://proxy:8080") {
		t.Errorf("proxy config missing\ngot: %s", joined)
	}
	if strings.Contains(joined, "sk-must-not-leak") {
		t.Errorf("a secret reached the runtime CLI environment\ngot: %s", joined)
	}
}

func TestParseAgentEnvRejectsMalformed(t *testing.T) {
	if _, err := ParseAgentEnv([]string{"NOEQUALS"}); err == nil {
		t.Fatal("ParseAgentEnv accepted a pair with no '='")
	}
	got, err := ParseAgentEnv([]string{"A=1", "B=two=parts"})
	if err != nil {
		t.Fatalf("ParseAgentEnv: %v", err)
	}
	if got["A"] != "1" || got["B"] != "two=parts" {
		t.Fatalf("parsed %v, want A=1 and B=two=parts", got)
	}
}

func TestInheritedAgentEnvTakesOnlyProviderKeys(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-provider")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "unrelated-cloud-secret")

	got := InheritedAgentEnv()
	if got["ANTHROPIC_API_KEY"] != "sk-provider" {
		t.Errorf("provider key not inherited: %v", got)
	}
	if _, ok := got["AWS_SECRET_ACCESS_KEY"]; ok {
		t.Errorf("unrelated host secret was handed to the agent: %v", got)
	}
}
