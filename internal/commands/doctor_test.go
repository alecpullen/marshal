package commands

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
)

// rowsText joins every row's Header, Text, Detail, and Desc for assertion
// matching. It does not recurse into Children.
func rowsText(rows []Row) string {
	var b strings.Builder
	for _, r := range rows {
		if r.Header != "" {
			b.WriteString(r.Header + " ")
		}
		b.WriteString(r.Text + " ")
		b.WriteString(r.Detail + " ")
		b.WriteString(r.Desc + " ")
	}
	return b.String()
}

func TestDoctorReportsCleanConfig(t *testing.T) {
	state := newTestState()
	res := doctorHandler(state, nil)
	if res.Doc == nil {
		t.Fatal("want a Doc result")
	}
	joined := rowsText(res.Doc.Rows)
	if !strings.Contains(strings.ToLower(joined), "no problems") {
		t.Errorf("clean config should say so, got %q", joined)
	}
}

func TestDoctorListsDiagnostics(t *testing.T) {
	state := newTestState()
	state.Config.Providers = map[string]config.ProviderConfig{
		"broken": {Type: "openai_compatible"}, // no base URL
	}
	res := doctorHandler(state, nil)
	if res.Doc == nil {
		t.Fatal("want a Doc result")
	}
	if !strings.Contains(rowsText(res.Doc.Rows), "providers.broken.base_url") {
		t.Errorf("diagnostic not listed: %q", rowsText(res.Doc.Rows))
	}
}

func TestDoctorNeverEchoesKeyMaterial(t *testing.T) {
	state := newTestState()
	state.Config.Providers = map[string]config.ProviderConfig{
		"p": {Type: "openai_compatible", BaseURL: "https://x.example/v1", APIKey: "sk-secret-value"},
	}
	res := doctorHandler(state, nil)
	if res.Doc != nil && strings.Contains(rowsText(res.Doc.Rows), "sk-secret-value") {
		t.Error("/doctor echoed key material")
	}
}
