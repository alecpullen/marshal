package config

import (
	"fmt"
	"os"
	"reflect"
	"strings"

	"marshal/internal/trust"

	"github.com/pelletier/go-toml/v2"
)

// Layers is the config at each merge level: built-in defaults, defaults+user
// config, and the fully merged result (defaults+user+project).
type Layers struct {
	Default  Config
	User     Config
	Merged   Config
	Migrated bool // true when MigrateLegacyAgentModel ran during LoadLayers
	// SubtaskIterationsSet reports whether either file layer explicitly set
	// [agent] subtask_iterations — needed because the merged int cannot
	// distinguish unset from an explicit 0 ("unlimited").
	SubtaskIterationsSet bool
}

// LoadLayers loads config and exposes the cumulative snapshots used for
// provenance. Load is a thin wrapper over this.
func LoadLayers(opts LoadOptions) (Layers, error) {
	cfg := Default()
	def := cfg

	home := opts.HomeDir
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return Layers{}, fmt.Errorf("find home directory: %w", err)
		}
	}

	work := opts.WorkingDir
	if work == "" {
		var err error
		work, err = os.Getwd()
		if err != nil {
			return Layers{}, fmt.Errorf("find working directory: %w", err)
		}
	}

	userPath := UserConfigPath(home)
	userFile, err := loadFile(userPath)
	if err != nil {
		return Layers{}, err
	}
	if err := merge(&cfg, userFile); err != nil {
		return Layers{}, fmt.Errorf("merge config %s: %w", userPath, err)
	}
	user := cfg

	projectPath := ProjectConfigPath(work)
	hasProject := trust.HasProjectConfig(work)
	applyProject := !opts.SkipProjectConfig
	if hasProject && opts.SkipProjectConfig && opts.Trusted != nil {
		*opts.Trusted = false
	}
	if hasProject && applyProject && opts.TrustResolver != nil {
		decision, derr := opts.TrustResolver.Resolve(work, true)
		if derr != nil {
			return Layers{}, fmt.Errorf("resolve project trust: %w", derr)
		}
		if decision == trust.DecisionDontTrust {
			applyProject = false
		} else if decision == trust.DecisionTrustPermanent {
			_ = opts.TrustResolver.Record(work, decision)
		}
		if opts.Trusted != nil {
			*opts.Trusted = decision == trust.DecisionTrustPermanent || decision == trust.DecisionTrustSession
		}
	}
	var projectFile configFile
	if applyProject {
		var err error
		projectFile, err = loadFile(projectPath)
		if err != nil {
			return Layers{}, err
		}
		if err := merge(&cfg, projectFile); err != nil {
			return Layers{}, fmt.Errorf("merge config %s: %w", projectPath, err)
		}
	}
	// The legacy [agent] provider/model pair is read from the raw file
	// mirror (the fields no longer exist on AgentConfig). The project file
	// wins over the user file when both name a pair. TODO: remove this
	// compatibility shim after one release cycle.
	legacyProvider, legacyModel := "", ""
	if projectFile.Agent != nil && projectFile.Agent.Provider != nil && projectFile.Agent.Model != nil {
		legacyProvider, legacyModel = *projectFile.Agent.Provider, *projectFile.Agent.Model
	} else if userFile.Agent != nil && userFile.Agent.Provider != nil && userFile.Agent.Model != nil {
		legacyProvider, legacyModel = *userFile.Agent.Provider, *userFile.Agent.Model
	}
	subtaskSet := (userFile.Agent != nil && userFile.Agent.SubtaskIterations != nil) ||
		(projectFile.Agent != nil && projectFile.Agent.SubtaskIterations != nil)
	// 12 was the old implicit default. Treat that legacy value as unset so
	// existing configs receive the current default instead of remaining pinned.
	if subtaskSet && cfg.Agent.SubtaskIterations == 12 {
		cfg.Agent.SubtaskIterations = 0
		subtaskSet = false
	}
	migrated := MigrateLegacyAgentModel(&cfg, legacyProvider, legacyModel)
	migrated = MigrateEmbeddingRoleBinding(&cfg) || migrated
	coercePresetPricing(&cfg)
	return Layers{Default: def, User: user, Merged: cfg, Migrated: migrated, SubtaskIterationsSet: subtaskSet}, nil
}

// LayerID identifies which merge layer supplied a value.
type LayerID int

const (
	LayerDefault LayerID = iota
	LayerUser
	LayerProject
)

func (l LayerID) String() string {
	switch l {
	case LayerUser:
		return "user config"
	case LayerProject:
		return "project config"
	default:
		return "default"
	}
}

// Provenance records which layer supplied a value and which lower layer's
// value it overrides (LayerDefault when nothing meaningful is overridden).
type Provenance struct {
	SetBy     LayerID
	Overrides LayerID
}

// ProvenanceOf compares the snapshots at a dotted TOML path.
func (l Layers) ProvenanceOf(dottedPath string) Provenance {
	merged, ok := LookupPath(l.Merged, dottedPath)
	if !ok {
		return Provenance{SetBy: LayerDefault, Overrides: LayerDefault}
	}
	user, _ := LookupPath(l.User, dottedPath)
	def, _ := LookupPath(l.Default, dottedPath)
	if !reflect.DeepEqual(merged, user) {
		p := Provenance{SetBy: LayerProject, Overrides: LayerDefault}
		if !reflect.DeepEqual(user, def) {
			p.Overrides = LayerUser
		}
		return p
	}
	if !reflect.DeepEqual(user, def) {
		return Provenance{SetBy: LayerUser, Overrides: LayerDefault}
	}
	return Provenance{SetBy: LayerDefault, Overrides: LayerDefault}
}

// LookupPath walks cfg by dotted TOML tags ("tools.shell.allow_network"),
// descending structs and string-keyed maps.
func LookupPath(cfg Config, dottedPath string) (any, bool) {
	var cur any = cfg
	for _, seg := range strings.Split(dottedPath, ".") {
		v := reflect.ValueOf(cur)
		for v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return nil, false
			}
			v = v.Elem()
		}
		switch v.Kind() {
		case reflect.Struct:
			field, ok := fieldByTOMLTag(v, seg)
			if !ok {
				return nil, false
			}
			cur = field.Interface()
		case reflect.Map:
			if v.Type().Key().Kind() != reflect.String {
				return nil, false
			}
			mv := v.MapIndex(reflect.ValueOf(seg))
			if !mv.IsValid() {
				return nil, false
			}
			cur = mv.Interface()
		default:
			return nil, false
		}
	}
	return cur, true
}

func fieldByTOMLTag(v reflect.Value, name string) (reflect.Value, bool) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("toml")
		if tag == "" {
			tag = strings.ToLower(t.Field(i).Name)
		}
		if tag == name || strings.Split(tag, ",")[0] == name {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}

// SaveUserConfigValue writes one value at a dotted TOML path into the user
// config file, preserving everything else in it. Used for global-target
// settings edits (person preferences, credentials).
func SaveUserConfigValue(path, dottedPath string, value any) error {
	file, err := loadFile(path)
	if err != nil {
		return fmt.Errorf("load user config: %w", err)
	}
	if err := setPath(&file, dottedPath, value); err != nil {
		return fmt.Errorf("set %s: %w", dottedPath, err)
	}
	data, err := toml.Marshal(&file)
	if err != nil {
		return fmt.Errorf("marshal user config: %w", err)
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		header := "# Marshal global configuration\n"
		data = append([]byte(header), data...)
	}
	return writeUserConfigFile(path, data)
}

// setPath sets value at a dotted TOML path inside the file mirror,
// allocating nil pointers and maps on the way down. Supports string, bool,
// and integer values (the person-preference/credential shapes).
func setPath(file *configFile, dottedPath string, value any) error {
	segs := strings.Split(dottedPath, ".")
	cur := reflect.ValueOf(file).Elem()
	for i, seg := range segs {
		last := i == len(segs)-1
		for cur.Kind() == reflect.Pointer {
			if cur.IsNil() {
				cur.Set(reflect.New(cur.Type().Elem()))
			}
			cur = cur.Elem()
		}
		switch cur.Kind() {
		case reflect.Struct:
			f, ok := fieldByTOMLTag(cur, seg)
			if !ok {
				return fmt.Errorf("unknown config path %q", dottedPath)
			}
			if last {
				return assignReflect(f, value)
			}
			cur = f
		case reflect.Map:
			if cur.Type().Key().Kind() != reflect.String {
				return fmt.Errorf("unsupported map at %q", seg)
			}
			if cur.IsNil() {
				cur.Set(reflect.MakeMap(cur.Type()))
			}
			elemType := cur.Type().Elem()
			mv := cur.MapIndex(reflect.ValueOf(seg))
			if !mv.IsValid() {
				nv := reflect.New(elemType).Elem()
				if last {
					if err := assignReflect(nv, value); err != nil {
						return err
					}
				}
				cur.SetMapIndex(reflect.ValueOf(seg), nv)
				mv = cur.MapIndex(reflect.ValueOf(seg))
			} else if last {
				nv := reflect.New(elemType).Elem()
				if err := assignReflect(nv, value); err != nil {
					return err
				}
				cur.SetMapIndex(reflect.ValueOf(seg), nv)
				return nil
			}
			// descend: maps are not addressable, so copy out, write back after struct descent
			tmp := reflect.New(elemType).Elem()
			tmp.Set(mv)
			cur.SetMapIndex(reflect.ValueOf(seg), tmp)
			parentMap := cur
			mapKey := reflect.ValueOf(seg)
			cur = tmp
			defer func(p reflect.Value, k reflect.Value, v reflect.Value) {
				p.SetMapIndex(k, v)
			}(parentMap, mapKey, tmp)
		default:
			return fmt.Errorf("cannot descend into %q", seg)
		}
	}
	return nil
}

func assignReflect(dst reflect.Value, value any) error {
	for dst.Kind() == reflect.Pointer {
		if dst.IsNil() {
			dst.Set(reflect.New(dst.Type().Elem()))
		}
		dst = dst.Elem()
	}
	src := reflect.ValueOf(value)
	if !src.IsValid() {
		return fmt.Errorf("nil value")
	}
	if src.Type().AssignableTo(dst.Type()) {
		dst.Set(src)
		return nil
	}
	if src.Type().ConvertibleTo(dst.Type()) && dst.Kind() >= reflect.Int && dst.Kind() <= reflect.Int64 {
		dst.Set(src.Convert(dst.Type()))
		return nil
	}
	return fmt.Errorf("cannot assign %T to %s", value, dst.Type())
}
