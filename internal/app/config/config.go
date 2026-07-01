package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Project  ProjectConfig  `toml:"project"`
	Commands CommandsConfig `toml:"commands"`
	Profile  ProfileConfig  `toml:"profile"`
	Privacy  PrivacyConfig  `toml:"privacy"`
	Indexing IndexingConfig `toml:"indexing"`
}

type ProjectConfig struct {
	Name      string   `toml:"name"`
	Languages []string `toml:"languages"`
}

type CommandsConfig struct {
	Test   string `toml:"test"`
	Format string `toml:"format"`
	Vet    string `toml:"vet"`
}

type ProfileConfig struct {
	Default string `toml:"default"`
}

type PrivacyConfig struct {
	RemoteProvidersAllowed bool `toml:"remote_providers_allowed"`
	RedactSecrets          bool `toml:"redact_secrets"`
	IncludeGitignoredFiles bool `toml:"include_gitignored_files"`
}

type IndexingConfig struct {
	UseTreesitter  bool     `toml:"use_treesitter"`
	UseEmbeddings  bool     `toml:"use_embeddings"`
	SummariseFiles bool     `toml:"summarise_files"`
	Ignore         []string `toml:"ignore"`
}

type LoadOptions struct {
	HomeDir    string
	WorkingDir string
}

type configFile struct {
	Project *struct {
		Name      *string  `toml:"name"`
		Languages []string `toml:"languages"`
	} `toml:"project"`
	Commands *struct {
		Test   *string `toml:"test"`
		Format *string `toml:"format"`
		Vet    *string `toml:"vet"`
	} `toml:"commands"`
	Profile *struct {
		Default *string `toml:"default"`
	} `toml:"profile"`
	Privacy *struct {
		RemoteProvidersAllowed *bool `toml:"remote_providers_allowed"`
		RedactSecrets          *bool `toml:"redact_secrets"`
		IncludeGitignoredFiles *bool `toml:"include_gitignored_files"`
	} `toml:"privacy"`
	Indexing *struct {
		UseTreesitter  *bool    `toml:"use_treesitter"`
		UseEmbeddings  *bool    `toml:"use_embeddings"`
		SummariseFiles *bool    `toml:"summarise_files"`
		Ignore         []string `toml:"ignore"`
	} `toml:"indexing"`
}

func Default() Config {
	return Config{
		Project: ProjectConfig{
			Name:      "marshal",
			Languages: []string{"go", "markdown"},
		},
		Commands: CommandsConfig{
			Test:   "go test ./...",
			Format: "gofmt -w .",
			Vet:    "go vet ./...",
		},
		Profile: ProfileConfig{
			Default: "local_balanced",
		},
		Privacy: PrivacyConfig{
			RemoteProvidersAllowed: false,
			RedactSecrets:          true,
			IncludeGitignoredFiles: false,
		},
		Indexing: IndexingConfig{
			UseTreesitter:  false,
			UseEmbeddings:  false,
			SummariseFiles: false,
			Ignore:         []string{"node_modules/**", "vendor/**", "dist/**", ".git/**"},
		},
	}
}

func Load(opts LoadOptions) (Config, error) {
	cfg := Default()

	home := opts.HomeDir
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return Config{}, fmt.Errorf("find home directory: %w", err)
		}
	}

	work := opts.WorkingDir
	if work == "" {
		var err error
		work, err = os.Getwd()
		if err != nil {
			return Config{}, fmt.Errorf("find working directory: %w", err)
		}
	}

	for _, path := range []string{
		filepath.Join(home, ".config", "marshal", "config.toml"),
		filepath.Join(work, ".marshal", "config.toml"),
	} {
		next, err := loadFile(path)
		if err != nil {
			return Config{}, err
		}
		merge(&cfg, next)
	}

	return cfg, nil
}

func loadFile(path string) (configFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return configFile{}, nil
		}
		return configFile{}, fmt.Errorf("read config %s: %w", path, err)
	}

	var file configFile
	if err := toml.Unmarshal(data, &file); err != nil {
		return configFile{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return file, nil
}

func merge(cfg *Config, file configFile) {
	if file.Project != nil {
		if file.Project.Name != nil {
			cfg.Project.Name = *file.Project.Name
		}
		if file.Project.Languages != nil {
			cfg.Project.Languages = file.Project.Languages
		}
	}
	if file.Commands != nil {
		if file.Commands.Test != nil {
			cfg.Commands.Test = *file.Commands.Test
		}
		if file.Commands.Format != nil {
			cfg.Commands.Format = *file.Commands.Format
		}
		if file.Commands.Vet != nil {
			cfg.Commands.Vet = *file.Commands.Vet
		}
	}
	if file.Profile != nil && file.Profile.Default != nil {
		cfg.Profile.Default = *file.Profile.Default
	}
	if file.Privacy != nil {
		if file.Privacy.RemoteProvidersAllowed != nil {
			cfg.Privacy.RemoteProvidersAllowed = *file.Privacy.RemoteProvidersAllowed
		}
		if file.Privacy.RedactSecrets != nil {
			cfg.Privacy.RedactSecrets = *file.Privacy.RedactSecrets
		}
		if file.Privacy.IncludeGitignoredFiles != nil {
			cfg.Privacy.IncludeGitignoredFiles = *file.Privacy.IncludeGitignoredFiles
		}
	}
	if file.Indexing != nil {
		if file.Indexing.UseTreesitter != nil {
			cfg.Indexing.UseTreesitter = *file.Indexing.UseTreesitter
		}
		if file.Indexing.UseEmbeddings != nil {
			cfg.Indexing.UseEmbeddings = *file.Indexing.UseEmbeddings
		}
		if file.Indexing.SummariseFiles != nil {
			cfg.Indexing.SummariseFiles = *file.Indexing.SummariseFiles
		}
		if file.Indexing.Ignore != nil {
			cfg.Indexing.Ignore = file.Indexing.Ignore
		}
	}
}
