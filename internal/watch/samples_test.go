package watch

import (
	"testing"
	"time"
)

func startSampleWatch(t *testing.T, condition string, mode Mode, samples []Sample) (*Manager, *watch) {
	t.Helper()
	m := newTestManager(t, Deps{})
	m.setSampler(&fakeSampler{samples: samples})
	id, _, err := m.Start(Spec{Name: "w", Kind: KindCommand, Command: "x", Condition: condition, Mode: mode, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return m, m.getWatch(id)
}

func TestSampleRingCapsAtMax(t *testing.T) {
	m, w := startSampleWatch(t, "regex never-matches", ModeRepeat, []Sample{{Stdout: "a"}})
	for i := 0; i < MaxSamples+12; i++ {
		m.sampleOnce(w)
	}
	info := w.snapshot()
	if len(info.Samples) != MaxSamples {
		t.Fatalf("samples = %d, want %d", len(info.Samples), MaxSamples)
	}
	for i := 1; i < len(info.Samples); i++ {
		if info.Samples[i].At.Before(info.Samples[i-1].At) {
			t.Fatal("samples not oldest first")
		}
	}
}

func TestSampleValueForms(t *testing.T) {
	cases := []struct {
		name    string
		cond    string
		s       Sample
		tripped bool
		want    float64
	}{
		{"exit code", "exit_code 0", Sample{ExitCode: 3}, false, 3},
		{"json numeric operand", "json a.b > 10", Sample{Stdout: `{"a":{"b":42.5}}`}, true, 42.5},
		{"json numeric string value", "json a > 1", Sample{Stdout: `{"a":"7"}`}, false, 7},
		{"json missing field", "json a > 1", Sample{Stdout: `{"z":1}`}, false, 0},
		{"json not json", "json a > 1", Sample{Stdout: `nope`}, false, 0},
		{"json string operand tripped", "json status = ok", Sample{Stdout: `{"status":"ok"}`}, true, 1},
		{"json string operand clear", "json status = ok", Sample{Stdout: `{"status":"bad"}`}, false, 0},
		{"regex tripped", "regex foo", Sample{Stdout: "foo"}, true, 1},
		{"change clear", "", Sample{Stdout: "x"}, false, 0},
	}
	for _, c := range cases {
		if got := sampleValue(c.cond, c.s, c.tripped); got != c.want {
			t.Errorf("%s: sampleValue = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSampleTrippedAndValueRecorded(t *testing.T) {
	m, w := startSampleWatch(t, "exit_code 2", ModeRepeat, []Sample{{ExitCode: 0}, {ExitCode: 2}})
	m.sampleOnce(w)
	m.sampleOnce(w)
	got := w.snapshot().Samples
	if len(got) != 2 || got[0].Tripped || got[0].Value != 0 || !got[1].Tripped || got[1].Value != 2 {
		t.Fatalf("samples = %+v", got)
	}
}

func TestChangeBaselineSampleNotTripped(t *testing.T) {
	m, w := startSampleWatch(t, "change", ModeRepeat, []Sample{{Stdout: "a"}, {Stdout: "b"}})
	m.sampleOnce(w)
	m.sampleOnce(w)
	got := w.snapshot().Samples
	if len(got) != 2 || got[0].Tripped || !got[1].Tripped {
		t.Fatalf("samples = %+v", got)
	}
}

func TestListReturnsSampleCopies(t *testing.T) {
	m, w := startSampleWatch(t, "regex x", ModeRepeat, []Sample{{Stdout: "a"}})
	m.sampleOnce(w)
	list := m.List()
	if len(list) != 1 || len(list[0].Samples) != 1 {
		t.Fatalf("list = %+v", list)
	}
	list[0].Samples[0].Value = 99
	list[0].Samples = append(list[0].Samples, SamplePoint{})
	if got := m.List()[0].Samples; len(got) != 1 || got[0].Value == 99 {
		t.Fatalf("manager state mutated: %+v", got)
	}
}
