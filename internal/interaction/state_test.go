package interaction

import (
	"strings"
	"testing"
)

func modePtr(m Mode) *Mode { return &m }

func boolPtr(b bool) *bool { return &b }

func TestApplyInteractionPatchTable(t *testing.T) {
	base := State{Mode: ModeAct, Revision: 7}
	ask := State{Mode: ModeAsk, Revision: 7}
	actAuto := State{Mode: ModeAct, Auto: true, Revision: 7}
	actAutoSystem := State{Mode: ModeAct, Auto: true, System: true, Revision: 7}
	askSystem := State{Mode: ModeAsk, System: true, Revision: 7}

	tests := []struct {
		name  string
		start State
		patch Patch
		want  State
	}{
		{
			name:  "switch to ask clears auto",
			start: actAutoSystem,
			patch: Patch{Mode: modePtr(ModeAsk)},
			want:  State{Mode: ModeAsk, System: true, Revision: 7},
		},
		{
			name:  "switch to act leaves auto off",
			start: ask,
			patch: Patch{Mode: modePtr(ModeAct)},
			want:  State{Mode: ModeAct, Revision: 7},
		},
		{
			name:  "switch to act preserves system access",
			start: askSystem,
			patch: Patch{Mode: modePtr(ModeAct)},
			want:  State{Mode: ModeAct, System: true, Revision: 7},
		},
		{
			name:  "auto on from act",
			start: base,
			patch: Patch{Auto: boolPtr(true)},
			want:  State{Mode: ModeAct, Auto: true, Revision: 7},
		},
		{
			name:  "auto on from ask promotes to act",
			start: ask,
			patch: Patch{Auto: boolPtr(true)},
			want:  State{Mode: ModeAct, Auto: true, Revision: 7},
		},
		{
			name:  "auto off from act",
			start: actAuto,
			patch: Patch{Auto: boolPtr(false)},
			want:  State{Mode: ModeAct, Revision: 7},
		},
		{
			name:  "auto off from ask is a no-op",
			start: ask,
			patch: Patch{Auto: boolPtr(false)},
			want:  State{Mode: ModeAsk, Revision: 7},
		},
		{
			name:  "system on leaves mode and auto untouched",
			start: actAuto,
			patch: Patch{System: boolPtr(true)},
			want:  State{Mode: ModeAct, Auto: true, System: true, Revision: 7},
		},
		{
			name:  "system off leaves mode and auto untouched",
			start: actAutoSystem,
			patch: Patch{System: boolPtr(false)},
			want:  State{Mode: ModeAct, Auto: true, Revision: 7},
		},
		{
			name:  "mode act plus auto on",
			start: ask,
			patch: Patch{Mode: modePtr(ModeAct), Auto: boolPtr(true)},
			want:  State{Mode: ModeAct, Auto: true, Revision: 7},
		},
		{
			name:  "mode act plus auto off",
			start: actAuto,
			patch: Patch{Mode: modePtr(ModeAct), Auto: boolPtr(false)},
			want:  State{Mode: ModeAct, Revision: 7},
		},
		{
			name:  "mode ask plus auto off",
			start: actAutoSystem,
			patch: Patch{Mode: modePtr(ModeAsk), Auto: boolPtr(false)},
			want:  State{Mode: ModeAsk, System: true, Revision: 7},
		},
		{
			name:  "all fields on",
			start: ask,
			patch: Patch{Mode: modePtr(ModeAct), Auto: boolPtr(true), System: boolPtr(true)},
			want:  State{Mode: ModeAct, Auto: true, System: true, Revision: 7},
		},
		{
			name:  "all fields off",
			start: actAutoSystem,
			patch: Patch{Mode: modePtr(ModeAsk), Auto: boolPtr(false), System: boolPtr(false)},
			want:  State{Mode: ModeAsk, Revision: 7},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Apply(tc.start, tc.patch)
			if err != nil {
				t.Fatalf("Apply(%+v, %+v) returned error: %v", tc.start, tc.patch, err)
			}
			if got != tc.want {
				t.Fatalf("Apply(%+v, %+v) = %+v, want %+v", tc.start, tc.patch, got, tc.want)
			}
			if got.Revision != tc.start.Revision {
				t.Fatalf("Apply changed revision: got %d, want %d", got.Revision, tc.start.Revision)
			}
		})
	}
}

func TestApplyInteractionPatchRejectsAtomically(t *testing.T) {
	valid := State{Mode: ModeAct, Revision: 3}
	ask := State{Mode: ModeAsk, Revision: 3}

	tests := []struct {
		name    string
		start   State
		patch   Patch
		wantErr string
	}{
		{
			name:    "empty patch",
			start:   valid,
			patch:   Patch{},
			wantErr: "empty interaction patch",
		},
		{
			name:    "invalid mode",
			start:   valid,
			patch:   Patch{Mode: modePtr(Mode("plan"))},
			wantErr: "invalid interaction mode",
		},
		{
			name:    "ask plus auto",
			start:   valid,
			patch:   Patch{Mode: modePtr(ModeAsk), Auto: boolPtr(true)},
			wantErr: "Ask cannot enable Auto",
		},
		{
			name:    "uninitialized state",
			start:   State{Mode: ModeAct},
			patch:   Patch{Auto: boolPtr(true)},
			wantErr: "not initialized",
		},
		{
			name:    "invalid starting mode",
			start:   State{Mode: Mode("edit"), Revision: 3},
			patch:   Patch{System: boolPtr(true)},
			wantErr: "invalid interaction mode",
		},
		{
			name:    "ask state with auto already set",
			start:   State{Mode: ModeAsk, Auto: true, Revision: 3},
			patch:   Patch{System: boolPtr(true)},
			wantErr: "Ask cannot enable Auto",
		},
		{
			name:    "ask plus auto on an ask state",
			start:   ask,
			patch:   Patch{Mode: modePtr(ModeAsk), Auto: boolPtr(true)},
			wantErr: "Ask cannot enable Auto",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Apply(tc.start, tc.patch)
			if err == nil {
				t.Fatalf("Apply(%+v, %+v) = %+v, want error containing %q", tc.start, tc.patch, got, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Apply error = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
			if got != tc.start {
				t.Fatalf("rejected Apply mutated state: got %+v, want original %+v", got, tc.start)
			}
		})
	}
}

func TestLegacyDefaultsMapping(t *testing.T) {
	tests := []struct {
		raw      string
		want     Defaults
		wantNote string
		wantErr  bool
	}{
		{raw: "plan", want: Defaults{Mode: ModeAsk}, wantNote: "planning remains a skill workflow"},
		{raw: "default", want: Defaults{Mode: ModeAsk}, wantNote: "planning remains a skill workflow"},
		{raw: "edit", want: Defaults{Mode: ModeAsk}, wantNote: "approval for every file change was retired"},
		{raw: "copilot", want: Defaults{Mode: ModeAct}, wantNote: "replaced with Act"},
		{raw: "auto", want: Defaults{Mode: ModeAct, Auto: true}, wantNote: "preauthorizes destructive workspace commands"},
		{raw: "  PLAN  ", want: Defaults{Mode: ModeAsk}, wantNote: "planning remains a skill workflow"},
		{raw: "Auto", want: Defaults{Mode: ModeAct, Auto: true}, wantNote: "preauthorizes destructive workspace commands"},
		{raw: "ask", wantErr: true},
		{raw: "act", wantErr: true},
		{raw: "yolo", wantErr: true},
		{raw: "", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			got, note, err := LegacyDefaults(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("LegacyDefaults(%q) = %+v, %q, want error", tc.raw, got, note)
				}
				if got != (Defaults{}) || note != "" {
					t.Fatalf("LegacyDefaults(%q) on error = %+v, %q, want zero values", tc.raw, got, note)
				}
				return
			}
			if err != nil {
				t.Fatalf("LegacyDefaults(%q) returned error: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("LegacyDefaults(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
			if !strings.Contains(note, tc.wantNote) {
				t.Fatalf("LegacyDefaults(%q) note = %q, want it to contain %q", tc.raw, note, tc.wantNote)
			}
			if err := ValidateDefaults(got); err != nil {
				t.Fatalf("LegacyDefaults(%q) produced invalid defaults %+v: %v", tc.raw, got, err)
			}
		})
	}
}

func TestBoundCeilingNeverWidens(t *testing.T) {
	all := Ceiling{Act: true, Auto: true, System: true}
	none := Ceiling{}
	actOnly := Ceiling{Act: true}
	actAuto := Ceiling{Act: true, Auto: true}
	actSystem := Ceiling{Act: true, System: true}

	tests := []struct {
		name    string
		state   State
		ceiling Ceiling
		want    State
	}{
		{
			name:    "no ceiling forces ask and drops auto and system",
			state:   State{Mode: ModeAct, Auto: true, System: true, Revision: 11},
			ceiling: none,
			want:    State{Mode: ModeAsk, Revision: 11},
		},
		{
			name:    "act only drops auto and system",
			state:   State{Mode: ModeAct, Auto: true, System: true, Revision: 11},
			ceiling: actOnly,
			want:    State{Mode: ModeAct, Revision: 11},
		},
		{
			name:    "act plus auto keeps auto but drops system",
			state:   State{Mode: ModeAct, Auto: true, System: true, Revision: 11},
			ceiling: actAuto,
			want:    State{Mode: ModeAct, Auto: true, Revision: 11},
		},
		{
			name:    "act plus system keeps system but drops auto",
			state:   State{Mode: ModeAct, Auto: true, System: true, Revision: 11},
			ceiling: actSystem,
			want:    State{Mode: ModeAct, System: true, Revision: 11},
		},
		{
			name:    "full ceiling is a no-op",
			state:   State{Mode: ModeAct, Auto: true, System: true, Revision: 11},
			ceiling: all,
			want:    State{Mode: ModeAct, Auto: true, System: true, Revision: 11},
		},
		{
			name:    "ask state stays ask under full ceiling",
			state:   State{Mode: ModeAsk, System: true, Revision: 11},
			ceiling: all,
			want:    State{Mode: ModeAsk, System: true, Revision: 11},
		},
		{
			name:    "auto cannot survive on an ask state",
			state:   State{Mode: ModeAsk, Auto: true, Revision: 11},
			ceiling: all,
			want:    State{Mode: ModeAsk, Revision: 11},
		},
		{
			name:    "zero state stays zero",
			state:   State{},
			ceiling: all,
			want:    State{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Bound(tc.state, tc.ceiling)
			if got != tc.want {
				t.Fatalf("Bound(%+v, %+v) = %+v, want %+v", tc.state, tc.ceiling, got, tc.want)
			}
			if got.Revision != tc.state.Revision {
				t.Fatalf("Bound changed revision: got %d, want %d", got.Revision, tc.state.Revision)
			}
			if got.Mode == ModeAct && !tc.ceiling.Act {
				t.Fatalf("Bound widened mode to act under ceiling %+v", tc.ceiling)
			}
			if got.Auto && (!tc.ceiling.Auto || !tc.state.Auto) {
				t.Fatalf("Bound widened auto under ceiling %+v", tc.ceiling)
			}
			if got.System && (!tc.ceiling.System || !tc.state.System) {
				t.Fatalf("Bound widened system access under ceiling %+v", tc.ceiling)
			}
		})
	}
}

func TestIntersectCeilings(t *testing.T) {
	all := Ceiling{Act: true, Auto: true, System: true}
	none := Ceiling{}
	actOnly := Ceiling{Act: true}
	actAuto := Ceiling{Act: true, Auto: true}
	actSystem := Ceiling{Act: true, System: true}

	tests := []struct {
		name string
		a, b Ceiling
		want Ceiling
	}{
		{name: "all with all", a: all, b: all, want: all},
		{name: "all with none", a: all, b: none, want: none},
		{name: "none with all", a: none, b: all, want: none},
		{name: "act only with act auto", a: actOnly, b: actAuto, want: actOnly},
		{name: "act auto with act system", a: actAuto, b: actSystem, want: actOnly},
		{name: "act system with act system", a: actSystem, b: actSystem, want: actSystem},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Intersect(tc.a, tc.b)
			if got != tc.want {
				t.Fatalf("Intersect(%+v, %+v) = %+v, want %+v", tc.a, tc.b, got, tc.want)
			}
			if rev := Intersect(tc.b, tc.a); rev != got {
				t.Fatalf("Intersect is not commutative: %+v vs %+v", got, rev)
			}
		})
	}
}
