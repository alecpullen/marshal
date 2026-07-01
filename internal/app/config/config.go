package config

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
