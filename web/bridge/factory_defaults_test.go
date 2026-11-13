package bridge

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestFactoryDefaultsApplyPerTypeUnsetRules pins the per-type definition of
// "unset" from the specification's §4.3: a string default applies to an empty
// handoff string, an int default to a zero one, a json.RawMessage default to a
// nil or zero-length one, and a check-list default only to a nil list. In
// every case a value the handoff sets itself wins, and a non-nil empty check
// list is an explicit choice that survives.
func TestFactoryDefaultsApplyPerTypeUnsetRules(t *testing.T) {
	defCheck := FactoryCheckKey{Kind: "check_run", Name: "build", AppID: 7}
	handoffCheck := FactoryCheckKey{Kind: "commit_status", Name: "gate", AppID: 0}

	bytesEqual := func(t *testing.T, got json.RawMessage, want string) {
		t.Helper()
		if !bytes.Equal(got, json.RawMessage(want)) {
			t.Fatalf("raw message = %q, want %q", got, want)
		}
	}

	cases := []struct {
		field string
		def   func(*FactoryDefaults)
		set   func(*FactoryConfig)
		// wantDefault asserts the default applied to the unset handoff.
		wantDefault func(*testing.T, FactoryConfig)
		// wantSet asserts the handoff's own value survived the merge.
		wantSet func(*testing.T, FactoryConfig)
	}{
		{
			field: "targetBranch",
			def:   func(d *FactoryDefaults) { d.TargetBranch = "main" },
			set:   func(c *FactoryConfig) { c.TargetBranch = "release" },
			wantDefault: func(t *testing.T, c FactoryConfig) {
				if c.TargetBranch != "main" {
					t.Fatalf("targetBranch = %q, want default %q", c.TargetBranch, "main")
				}
			},
			wantSet: func(t *testing.T, c FactoryConfig) {
				if c.TargetBranch != "release" {
					t.Fatalf("targetBranch = %q, want handoff %q", c.TargetBranch, "release")
				}
			},
		},
		{
			field: "workspace",
			def:   func(d *FactoryDefaults) { d.Workspace = "ws://published/example" },
			set:   func(c *FactoryConfig) { c.Workspace = "ws://handoff/example" },
			wantDefault: func(t *testing.T, c FactoryConfig) {
				if c.Workspace != "ws://published/example" {
					t.Fatalf("workspace = %q, want default %q", c.Workspace, "ws://published/example")
				}
			},
			wantSet: func(t *testing.T, c FactoryConfig) {
				if c.Workspace != "ws://handoff/example" {
					t.Fatalf("workspace = %q, want handoff %q", c.Workspace, "ws://handoff/example")
				}
			},
		},
		{
			field: "hostId",
			def:   func(d *FactoryDefaults) { d.HostID = "local" },
			set:   func(c *FactoryConfig) { c.HostID = "remote" },
			wantDefault: func(t *testing.T, c FactoryConfig) {
				if c.HostID != "local" {
					t.Fatalf("hostId = %q, want default %q", c.HostID, "local")
				}
			},
			wantSet: func(t *testing.T, c FactoryConfig) {
				if c.HostID != "remote" {
					t.Fatalf("hostId = %q, want handoff %q", c.HostID, "remote")
				}
			},
		},
		{
			field: "implementerRouting",
			def: func(d *FactoryDefaults) {
				d.ImplementerRouting = json.RawMessage(`{"profile":"frontier"}`)
			},
			set: func(c *FactoryConfig) { c.ImplementerRouting = json.RawMessage(`{}`) },
			wantDefault: func(t *testing.T, c FactoryConfig) {
				bytesEqual(t, c.ImplementerRouting, `{"profile":"frontier"}`)
			},
			wantSet: func(t *testing.T, c FactoryConfig) {
				bytesEqual(t, c.ImplementerRouting, `{}`)
			},
		},
		{
			field: "implementerRoutingNull",
			def: func(d *FactoryDefaults) {
				d.ImplementerRouting = json.RawMessage(`{"profile":"frontier"}`)
			},
			set: func(c *FactoryConfig) { c.ImplementerRouting = json.RawMessage(`null`) },
			wantDefault: func(t *testing.T, c FactoryConfig) {
				bytesEqual(t, c.ImplementerRouting, `{"profile":"frontier"}`)
			},
			wantSet: func(t *testing.T, c FactoryConfig) {
				bytesEqual(t, c.ImplementerRouting, `null`)
			},
		},
		{
			field: "reviewerRouting",
			def: func(d *FactoryDefaults) {
				d.ReviewerRouting = json.RawMessage(`{"profile":"frontier"}`)
			},
			set: func(c *FactoryConfig) { c.ReviewerRouting = json.RawMessage(`{}`) },
			wantDefault: func(t *testing.T, c FactoryConfig) {
				bytesEqual(t, c.ReviewerRouting, `{"profile":"frontier"}`)
			},
			wantSet: func(t *testing.T, c FactoryConfig) {
				bytesEqual(t, c.ReviewerRouting, `{}`)
			},
		},
		{
			field: "reviewerRoutingNull",
			def: func(d *FactoryDefaults) {
				d.ReviewerRouting = json.RawMessage(`{"profile":"frontier"}`)
			},
			set: func(c *FactoryConfig) { c.ReviewerRouting = json.RawMessage(`null`) },
			wantDefault: func(t *testing.T, c FactoryConfig) {
				bytesEqual(t, c.ReviewerRouting, `{"profile":"frontier"}`)
			},
			wantSet: func(t *testing.T, c FactoryConfig) {
				bytesEqual(t, c.ReviewerRouting, `null`)
			},
		},
		{
			field: "reviewerInstructions",
			def:   func(d *FactoryDefaults) { d.ReviewerInstructions = "be terse" },
			set:   func(c *FactoryConfig) { c.ReviewerInstructions = "be thorough" },
			wantDefault: func(t *testing.T, c FactoryConfig) {
				if c.ReviewerInstructions != "be terse" {
					t.Fatalf("reviewerInstructions = %q, want default %q", c.ReviewerInstructions, "be terse")
				}
			},
			wantSet: func(t *testing.T, c FactoryConfig) {
				if c.ReviewerInstructions != "be thorough" {
					t.Fatalf("reviewerInstructions = %q, want handoff %q", c.ReviewerInstructions, "be thorough")
				}
			},
		},
		{
			field: "expectedChecks",
			def:   func(d *FactoryDefaults) { d.ExpectedChecks = []FactoryCheckKey{defCheck} },
			set:   func(c *FactoryConfig) { c.ExpectedChecks = []FactoryCheckKey{} },
			wantDefault: func(t *testing.T, c FactoryConfig) {
				if c.ExpectedChecks == nil {
					t.Fatal("expectedChecks = nil, want the default's non-nil list")
				}
				if len(c.ExpectedChecks) != 1 || c.ExpectedChecks[0] != defCheck {
					t.Fatalf("expectedChecks = %+v, want the default's list", c.ExpectedChecks)
				}
			},
			wantSet: func(t *testing.T, c FactoryConfig) {
				if c.ExpectedChecks == nil {
					t.Fatal("expectedChecks = nil, want the handoff's explicitly empty list")
				}
				if len(c.ExpectedChecks) != 0 {
					t.Fatalf("expectedChecks = %+v, want an empty list", c.ExpectedChecks)
				}
			},
		},
		{
			field: "expectedChecksOverride",
			def:   func(d *FactoryDefaults) { d.ExpectedChecks = []FactoryCheckKey{defCheck} },
			set: func(c *FactoryConfig) {
				c.ExpectedChecks = []FactoryCheckKey{handoffCheck}
			},
			wantDefault: func(t *testing.T, c FactoryConfig) {
				if len(c.ExpectedChecks) != 1 || c.ExpectedChecks[0] != defCheck {
					t.Fatalf("expectedChecks = %+v, want the default's list", c.ExpectedChecks)
				}
			},
			wantSet: func(t *testing.T, c FactoryConfig) {
				if len(c.ExpectedChecks) != 1 || c.ExpectedChecks[0] != handoffCheck {
					t.Fatalf("expectedChecks = %+v, want the handoff's list", c.ExpectedChecks)
				}
			},
		},
		{
			field: "maxRounds",
			def:   func(d *FactoryDefaults) { d.MaxRounds = 9 },
			set:   func(c *FactoryConfig) { c.MaxRounds = 3 },
			wantDefault: func(t *testing.T, c FactoryConfig) {
				if c.MaxRounds != 9 {
					t.Fatalf("maxRounds = %d, want default %d", c.MaxRounds, 9)
				}
			},
			wantSet: func(t *testing.T, c FactoryConfig) {
				if c.MaxRounds != 3 {
					t.Fatalf("maxRounds = %d, want handoff %d", c.MaxRounds, 3)
				}
			},
		},
		{
			field: "ciPollSeconds",
			def:   func(d *FactoryDefaults) { d.CIPollSeconds = 7 },
			set:   func(c *FactoryConfig) { c.CIPollSeconds = 2 },
			wantDefault: func(t *testing.T, c FactoryConfig) {
				if c.CIPollSeconds != 7 {
					t.Fatalf("ciPollSeconds = %d, want default %d", c.CIPollSeconds, 7)
				}
			},
			wantSet: func(t *testing.T, c FactoryConfig) {
				if c.CIPollSeconds != 2 {
					t.Fatalf("ciPollSeconds = %d, want handoff %d", c.CIPollSeconds, 2)
				}
			},
		},
		{
			field: "ciWaitSeconds",
			def:   func(d *FactoryDefaults) { d.CIWaitSeconds = 900 },
			set:   func(c *FactoryConfig) { c.CIWaitSeconds = 60 },
			wantDefault: func(t *testing.T, c FactoryConfig) {
				if c.CIWaitSeconds != 900 {
					t.Fatalf("ciWaitSeconds = %d, want default %d", c.CIWaitSeconds, 900)
				}
			},
			wantSet: func(t *testing.T, c FactoryConfig) {
				if c.CIWaitSeconds != 60 {
					t.Fatalf("ciWaitSeconds = %d, want handoff %d", c.CIWaitSeconds, 60)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			d := &FactoryDefaults{}
			tc.def(d)

			tc.wantDefault(t, factoryMergeDefaults(FactoryConfig{}, d))

			handoff := FactoryConfig{}
			tc.set(&handoff)
			tc.wantSet(t, factoryMergeDefaults(handoff, d))
		})
	}

	// A zero-length raw message is unset, exactly as the factory preflight
	// treats it.
	d := &FactoryDefaults{ImplementerRouting: json.RawMessage(`{"profile":"frontier"}`)}
	if got := factoryMergeDefaults(FactoryConfig{ImplementerRouting: json.RawMessage{}}, d); !bytes.Equal(got.ImplementerRouting, d.ImplementerRouting) {
		t.Fatalf("zero-length implementerRouting = %q, want the default", got.ImplementerRouting)
	}

	// A nil handoff check list inherits, and the inherited list is a copy:
	// mutating it must never reach the stored defaults.
	d = &FactoryDefaults{ExpectedChecks: []FactoryCheckKey{defCheck}}
	got := factoryMergeDefaults(FactoryConfig{}, d)
	if got.ExpectedChecks == nil {
		t.Fatal("expectedChecks = nil, want the default's list")
	}
	if &got.ExpectedChecks[0] == &d.ExpectedChecks[0] {
		t.Fatal("expectedChecks aliases the defaults' backing array")
	}
	got.ExpectedChecks[0].Name = "mutated"
	if d.ExpectedChecks[0].Name != "build" {
		t.Fatalf("stored default mutated to %q", d.ExpectedChecks[0].Name)
	}

	// A nil default leaves the handoff untouched, including check-list
	// nil-ness.
	handoff := FactoryConfig{
		RepoID:         "repo-1",
		ProjectRoot:    "/repo",
		Native:         true,
		TargetBranch:   "main",
		ExpectedChecks: nil,
	}
	if got := factoryMergeDefaults(handoff, nil); !reflect.DeepEqual(got, handoff) {
		t.Fatalf("nil defaults changed the handoff: %+v, want %+v", got, handoff)
	}
}

// TestFactoryDefaultsNeverSupplyNonInheritableFields pins that the type is the
// rule: the five non-inheritable fields are structurally absent, and a merge
// never invents a value for them.
func TestFactoryDefaultsNeverSupplyNonInheritableFields(t *testing.T) {
	typ := reflect.TypeOf(FactoryDefaults{})
	present := make(map[string]bool, typ.NumField())
	names := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		present[name] = true
		names = append(names, name)
	}
	for _, name := range []string{"RepoID", "ProjectRoot", "Native", "ImplementerAuto", "NoRequiredCIAcknowledged"} {
		if present[name] {
			t.Fatalf("FactoryDefaults carries non-inheritable field %q", name)
		}
	}
	want := []string{"TargetBranch", "Workspace", "HostID", "ImplementerRouting", "ReviewerRouting", "ReviewerInstructions", "ExpectedChecks", "MaxRounds", "CIPollSeconds", "CIWaitSeconds"}
	if len(names) != len(want) {
		t.Fatalf("FactoryDefaults fields = %v, want exactly %v", names, want)
	}
	for _, name := range want {
		if !present[name] {
			t.Fatalf("FactoryDefaults is missing inheritable field %q (has %v)", name, names)
		}
	}

	full := &FactoryDefaults{
		TargetBranch:         "main",
		Workspace:            "ws://published/example",
		HostID:               "local",
		ImplementerRouting:   json.RawMessage(`{"profile":"frontier"}`),
		ReviewerRouting:      json.RawMessage(`{"profile":"frontier"}`),
		ReviewerInstructions: "be terse",
		ExpectedChecks:       []FactoryCheckKey{{Kind: "check_run", Name: "build", AppID: 7}},
		MaxRounds:            9,
		CIPollSeconds:        7,
		CIWaitSeconds:        900,
	}

	set := FactoryConfig{
		RepoID:                   "repo-1",
		ProjectRoot:              "/path/to/project",
		Native:                   true,
		ImplementerAuto:          true,
		NoRequiredCIAcknowledged: true,
	}
	got := factoryMergeDefaults(set, full)
	if got.RepoID != "repo-1" || got.ProjectRoot != "/path/to/project" || !got.Native || !got.ImplementerAuto || !got.NoRequiredCIAcknowledged {
		t.Fatalf("merge changed non-inheritable fields: %+v", got)
	}

	// The same holds when the handoff sets them false: a default never turns
	// one of them true.
	unset := FactoryConfig{RepoID: "repo-1", ProjectRoot: "/path/to/project"}
	got = factoryMergeDefaults(unset, full)
	if got.Native || got.ImplementerAuto || got.NoRequiredCIAcknowledged {
		t.Fatalf("merge supplied a non-inheritable field: %+v", got)
	}
	if got.RepoID != "repo-1" || got.ProjectRoot != "/path/to/project" {
		t.Fatalf("merge changed identity fields: %+v", got)
	}
}

// TestFactoryValidateDefaults pins the validation subset from the
// specification's §4.6: nil is valid, the limits are not negative, the host is
// local when set, and each expected check carries a valid identity. The
// validator never fills anything in.
func TestFactoryValidateDefaults(t *testing.T) {
	cases := []struct {
		name    string
		d       *FactoryDefaults
		wantErr string // substring the error must name; "" means valid
	}{
		{name: "nil"},
		{name: "zero value", d: &FactoryDefaults{}},
		{
			name:    "negative maxRounds",
			d:       &FactoryDefaults{MaxRounds: -1},
			wantErr: "maxRounds",
		},
		{
			name:    "negative ciPollSeconds",
			d:       &FactoryDefaults{CIPollSeconds: -1},
			wantErr: "ciPollSeconds",
		},
		{
			name:    "negative ciWaitSeconds",
			d:       &FactoryDefaults{CIWaitSeconds: -1},
			wantErr: "ciWaitSeconds",
		},
		{
			name:    "non-local host",
			d:       &FactoryDefaults{HostID: "remote"},
			wantErr: "hostId",
		},
		{
			name: "local host",
			d:    &FactoryDefaults{HostID: "local"},
		},
		{
			name:    "check without a name",
			d:       &FactoryDefaults{ExpectedChecks: []FactoryCheckKey{{Kind: "check_run", AppID: 7}}},
			wantErr: "expectedChecks",
		},
		{
			name:    "check with an unknown kind",
			d:       &FactoryDefaults{ExpectedChecks: []FactoryCheckKey{{Kind: "issue", Name: "build", AppID: 7}}},
			wantErr: "expectedChecks",
		},
		{
			name:    "check_run without an app id",
			d:       &FactoryDefaults{ExpectedChecks: []FactoryCheckKey{{Kind: "check_run", Name: "build"}}},
			wantErr: "expectedChecks",
		},
		{
			name:    "commit_status with an app id",
			d:       &FactoryDefaults{ExpectedChecks: []FactoryCheckKey{{Kind: "commit_status", Name: "gate", AppID: 7}}},
			wantErr: "expectedChecks",
		},
		{
			name: "valid checks and limits",
			d: &FactoryDefaults{
				TargetBranch:         "main",
				Workspace:            "ws://published/example",
				HostID:               "local",
				ImplementerRouting:   json.RawMessage(`{"profile":"frontier"}`),
				ReviewerRouting:      json.RawMessage(`{"profile":"frontier"}`),
				ReviewerInstructions: "be terse",
				ExpectedChecks: []FactoryCheckKey{
					{Kind: "check_run", Name: "build", AppID: 7},
					{Kind: "commit_status", Name: "gate"},
				},
				MaxRounds:     9,
				CIPollSeconds: 7,
				CIWaitSeconds: 900,
			},
		},
		{name: "empty check list"},
		{
			name: "explicitly empty check list",
			d:    &FactoryDefaults{ExpectedChecks: []FactoryCheckKey{}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := factoryValidateDefaults(tc.d)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("factoryValidateDefaults() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("factoryValidateDefaults() = nil, want an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not name %q", err, tc.wantErr)
			}
		})
	}

	// The validator never fills the built-in limits in: a zero stays zero, so
	// a handoff can still inherit the default.
	d := &FactoryDefaults{}
	if err := factoryValidateDefaults(d); err != nil {
		t.Fatalf("factoryValidateDefaults() = %v", err)
	}
	if d.MaxRounds != 0 || d.CIPollSeconds != 0 || d.CIWaitSeconds != 0 || d.HostID != "" {
		t.Fatalf("validator filled the defaults: %+v", d)
	}
}

// assertPreSliceConfiguration asserts, field by field, the configuration the
// bootstrap factory produced for a handoff that states its own target branch
// and routings, before project defaults existed. The host and the three
// limits are the values normalisation contributes, so they appear here even
// though the handoff never set them.
func assertPreSliceConfiguration(t *testing.T, got FactoryConfig, root string) {
	t.Helper()
	want := FactoryConfig{
		RepoID:                   "repo-1",
		ProjectRoot:              root,
		HostID:                   "local",
		TargetBranch:             "main",
		Native:                   true,
		ImplementerRouting:       factoryDefaultsRouting(),
		ReviewerRouting:          factoryDefaultsRouting(),
		NoRequiredCIAcknowledged: true,
		MaxRounds:                5,
		CIPollSeconds:            15,
		CIWaitSeconds:            1800,
	}
	if got.RepoID != want.RepoID {
		t.Errorf("repoId = %q, want %q", got.RepoID, want.RepoID)
	}
	if got.ProjectRoot != want.ProjectRoot {
		t.Errorf("projectRoot = %q, want %q", got.ProjectRoot, want.ProjectRoot)
	}
	if got.HostID != want.HostID {
		t.Errorf("hostId = %q, want the built-in %q", got.HostID, want.HostID)
	}
	if got.TargetBranch != want.TargetBranch {
		t.Errorf("targetBranch = %q, want %q", got.TargetBranch, want.TargetBranch)
	}
	if got.Workspace != "" {
		t.Errorf("workspace = %q, want empty", got.Workspace)
	}
	if got.Native != want.Native {
		t.Errorf("native = %v, want %v", got.Native, want.Native)
	}
	if !bytes.Equal(got.ImplementerRouting, want.ImplementerRouting) {
		t.Errorf("implementerRouting = %q, want %q", got.ImplementerRouting, want.ImplementerRouting)
	}
	if !bytes.Equal(got.ReviewerRouting, want.ReviewerRouting) {
		t.Errorf("reviewerRouting = %q, want %q", got.ReviewerRouting, want.ReviewerRouting)
	}
	if got.ReviewerInstructions != "" {
		t.Errorf("reviewerInstructions = %q, want empty", got.ReviewerInstructions)
	}
	if got.ImplementerAuto {
		t.Error("implementerAuto = true, want false")
	}
	if got.ExpectedChecks != nil {
		t.Errorf("expectedChecks = %+v, want nil", got.ExpectedChecks)
	}
	if got.NoRequiredCIAcknowledged != want.NoRequiredCIAcknowledged {
		t.Errorf("noRequiredCiAcknowledged = %v, want %v", got.NoRequiredCIAcknowledged, want.NoRequiredCIAcknowledged)
	}
	if got.MaxRounds != want.MaxRounds {
		t.Errorf("maxRounds = %d, want the built-in %d", got.MaxRounds, want.MaxRounds)
	}
	if got.CIPollSeconds != want.CIPollSeconds {
		t.Errorf("ciPollSeconds = %d, want the built-in %d", got.CIPollSeconds, want.CIPollSeconds)
	}
	if got.CIWaitSeconds != want.CIWaitSeconds {
		t.Errorf("ciWaitSeconds = %d, want the built-in %d", got.CIWaitSeconds, want.CIWaitSeconds)
	}
	// Finally the whole configuration, which also pins nil-versus-empty for
	// the fields asserted above individually.
	sameFactoryConfig(t, got, want)
}

// TestProjectWithoutDefaultsBehavesExactlyAsToday pins the specification's
// §6 and §10 guarantee that project defaults are purely additive: where there
// are none to apply, the create path produces exactly the configuration the
// bootstrap factory produced before this slice.
//
// Three states are indistinguishable to resolution — no settings record at
// all, a record whose FactoryDefaults is nil, and an all-zero FactoryDefaults
// object — so all three are pinned against the same field-by-field
// expectation, including the built-in host, round count and CI limits that
// normalisation contributes.
func TestProjectWithoutDefaultsBehavesExactlyAsToday(t *testing.T) {
	f := newFactoryDefaultsFixture(t)
	// A settings record that exists and carries an intake mapping but no
	// factory defaults: the intake project must not stand in for them.
	nilDefaults := t.TempDir()
	if err := f.fleet.ws.AddProject(nilDefaults); err != nil {
		t.Fatal(err)
	}
	f.putSettings(t, nilDefaults, ProjectSettings{Intake: ProjectIntake{RepoID: "repo-1"}})
	// An all-zero defaults object: present, valid, and contributing nothing.
	zeroDefaults := t.TempDir()
	if err := f.fleet.ws.AddProject(zeroDefaults); err != nil {
		t.Fatal(err)
	}
	f.putSettings(t, zeroDefaults, ProjectSettings{FactoryDefaults: &FactoryDefaults{}})

	for _, tc := range []struct {
		key  string
		name string
		root string
	}{
		// f.root is registered and trusted, and nothing has ever been stored
		// for it: the fixture registers the repository, not its settings.
		{key: "no-record", name: "no settings record", root: f.root},
		{key: "nil-defaults", name: "nil factory defaults", root: nilDefaults},
		{key: "zero-defaults", name: "zero-valued factory defaults", root: zeroDefaults},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := f.handoff("no-defaults-"+tc.key, tc.root)
			// The fields a handoff had to state itself before this slice,
			// because nothing else could supply them.
			req.Config.TargetBranch = "main"
			req.Config.ImplementerRouting = factoryDefaultsRouting()
			req.Config.ReviewerRouting = factoryDefaultsRouting()
			item := f.created(t, req)
			assertPreSliceConfiguration(t, item.Draft.Config, tc.root)
			// The stored item carries the same configuration the response
			// showed; nothing rewrites it afterwards.
			stored, err := f.store.Get(item.ID)
			if err != nil {
				t.Fatal(err)
			}
			assertPreSliceConfiguration(t, stored.Draft.Config, tc.root)
			if item.State != "draft" || stored.State != "draft" {
				t.Fatalf("state = %q/%q, want draft", item.State, stored.State)
			}
		})
	}
}
