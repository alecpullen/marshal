package workspacecfg

import (
	"reflect"
	"strings"
	"testing"
)

func TestParsePreview(t *testing.T) {
	d, secs, diags := Parse([]byte("[policy]\nmode = \"edit\"\n\n[preview]\nports = [3000, 8080]\n"))
	if len(diags) != 0 {
		t.Fatalf("diags = %+v", diags)
	}
	if !reflect.DeepEqual(d.Preview.Ports, []int{3000, 8080}) {
		t.Fatalf("ports = %v", d.Preview.Ports)
	}
	found := false
	for _, s := range secs {
		if s.Key == "preview" {
			found = true
			if s.Layer != 0 || s.StartLine != 4 || s.EndLine != 5 {
				t.Fatalf("preview section = %+v", s)
			}
		}
	}
	if !found {
		t.Fatalf("no preview section in %+v", secs)
	}
}

func TestPreviewValidation(t *testing.T) {
	for name, ports := range map[string]string{
		"zero":      "[0]",
		"negative":  "[-1]",
		"too big":   "[65536]",
		"duplicate": "[3000, 3000]",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, diags := Parse([]byte("[workspace]\nname = \"x\"\n\n[preview]\nports = " + ports + "\n"))
			errs := errorsOf(diags)
			if len(errs) != 1 || errs[0].Line != 4 {
				t.Fatalf("diags = %+v, want one error on the [preview] header (line 4)", diags)
			}
		})
	}
	if _, _, diags := Parse([]byte("[preview]\nports = [1, 65535]\n")); len(diags) != 0 {
		t.Fatalf("boundary ports rejected: %+v", diags)
	}
}

func TestPatchLayer0PreviewInsertsSection(t *testing.T) {
	src := "[policy]\nmode = \"edit\"\n\n[setup]\nrun = \"make\"\n"
	out := mustPatch(t, src, 0, `{"preview":{"ports":[3000]}}`)
	d, _, diags := Parse([]byte(out))
	if len(diags) != 0 {
		t.Fatalf("diags = %+v\n%s", diags, out)
	}
	if !reflect.DeepEqual(d.Preview.Ports, []int{3000}) || d.Policy.Mode != "edit" || d.Setup.Run != "make" {
		t.Fatalf("doc = %+v\n%s", d, out)
	}
	// Canonical order: [preview] right after [policy], before [setup].
	if p, v, s := strings.Index(out, "[policy]"), strings.Index(out, "[preview]"), strings.Index(out, "[setup]"); !(p < v && v < s) {
		t.Fatalf("section order wrong:\n%s", out)
	}
}

func TestPatchLayer0PolicyKeepsPreview(t *testing.T) {
	src := "[policy]\nmode = \"edit\"\n\n[preview]\nports = [3000]\n"
	out := mustPatch(t, src, 0, `{"mode":"plan"}`)
	d, _, _ := Parse([]byte(out))
	if d.Policy.Mode != "plan" || !reflect.DeepEqual(d.Preview.Ports, []int{3000}) {
		t.Fatalf("doc = %+v\n%s", d, out)
	}
}

func TestPatchLayer0ClearsPreview(t *testing.T) {
	src := "[policy]\nmode = \"edit\"\n\n[preview]\nports = [3000]\n"
	out := mustPatch(t, src, 0, `{"preview":{"ports":[]}}`)
	if strings.Contains(out, "[preview]") {
		t.Fatalf("preview not removed:\n%s", out)
	}
	if d, _, _ := Parse([]byte(out)); d.Policy.Mode != "edit" {
		t.Fatalf("policy lost:\n%s", out)
	}
}

func TestPatchLayer0BothHalves(t *testing.T) {
	out := mustPatch(t, "[network]\nmode = \"off\"\n", 0, `{"mode":"auto","preview":{"ports":[8080]}}`)
	d, _, diags := Parse([]byte(out))
	if len(diags) != 0 || d.Policy.Mode != "auto" || !reflect.DeepEqual(d.Preview.Ports, []int{8080}) || d.Network.Mode != "off" {
		t.Fatalf("doc = %+v diags = %+v\n%s", d, diags, out)
	}
}

func TestFormatIncludesPreviewAfterPolicy(t *testing.T) {
	out := string(Format(Doc{Policy: Policy{Mode: "edit"}, Preview: Preview{Ports: []int{3000}}, Setup: Setup{Run: "x"}}))
	if p, v, s := strings.Index(out, "[policy]"), strings.Index(out, "[preview]"), strings.Index(out, "[setup]"); !(p >= 0 && p < v && v < s) {
		t.Fatalf("order wrong:\n%s", out)
	}
}
