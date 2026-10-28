package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	DefaultConfigFilename = "config.yaml"
	DefaultDirName        = ".devtrail"
	CurrentVersion        = 1
)

type Config struct {
	Version int            `yaml:"version"`
	Project ProjectConfig  `yaml:"project"`
	Storage StorageConfig  `yaml:"storage"`
	Privacy PrivacyConfig  `yaml:"privacy"`
	Reports ReportsConfig  `yaml:"reports"`
	Output  OutputConfig   `yaml:"output"`
}

type ProjectConfig struct {
	Name           string   `yaml:"name"`
	DefaultBranch  string   `yaml:"default_branch"`
	Timezone       string   `yaml:"timezone"`
	TargetBranches []string `yaml:"target_branches"`
}

type StorageConfig struct {
	Database   string `yaml:"database"`
	ReportsDir string `yaml:"reports_dir"`
	EventsDir  string `yaml:"events_dir"`
}

type PrivacyConfig struct {
	NetworkAccess           string `yaml:"network_access"`
	IncludeFullDiffs        bool   `yaml:"include_full_diffs"`
	IncludeUncommittedDiff  bool   `yaml:"include_uncommitted_diff"`
	IncludeAgentTranscripts bool   `yaml:"include_agent_transcripts"`
	IncludeSourceContents   bool   `yaml:"include_source_contents"`
	RedactSecrets           bool   `yaml:"redact_secrets"`
}

type ReportsConfig struct {
	Archive      bool             `yaml:"archive"`
	UpdateLatest bool             `yaml:"update_latest"`
	Defaults     ReportDefaults   `yaml:"defaults"`
	Schedules    []ScheduleConfig `yaml:"schedules"`
	Triggers     []TriggerConfig  `yaml:"triggers"`
}

type ReportDefaults struct {
	ActivityBranch string   `yaml:"activity_branch"`
	ComparisonBase string   `yaml:"comparison_base"`
	Include        []string `yaml:"include"`
	AISummary      string   `yaml:"ai_summary"`
}

type ScheduleConfig struct {
	Name      string `yaml:"name"`
	Enabled   bool   `yaml:"enabled"`
	Frequency string `yaml:"frequency"`
	At        string `yaml:"at,omitempty"`
	Day       string `yaml:"day,omitempty"`
	Month     string `yaml:"month,omitempty"`
	Type      string `yaml:"type"`
	Window    string `yaml:"window"`
	CatchUp   string `yaml:"catch_up,omitempty"`
}

type TriggerConfig struct {
	Name            string `yaml:"name"`
	Enabled         bool   `yaml:"enabled"`
	Event           string `yaml:"event"`
	Target          string `yaml:"target,omitempty"`
	ReportType      string `yaml:"report_type"`
	Mode            string `yaml:"mode,omitempty"`
	DebounceSeconds int    `yaml:"debounce_seconds,omitempty"`
}

type OutputConfig struct {
	Formats      []string `yaml:"formats"`
	Archive      bool     `yaml:"archive"`
	UpdateLatest bool     `yaml:"update_latest"`
}

// DefaultConfig generates a ready-to-use configuration for a given project name.
func DefaultConfig(projectName, defaultBranch string) *Config {
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	if projectName == "" {
		projectName = "my-project"
	}

	return &Config{
		Version: CurrentVersion,
		Project: ProjectConfig{
			Name:           projectName,
			DefaultBranch:  defaultBranch,
			Timezone:       "UTC",
			TargetBranches: []string{defaultBranch, "develop"},
		},
		Storage: StorageConfig{
			Database:   filepath.Join(DefaultDirName, "state.db"),
			ReportsDir: filepath.Join(DefaultDirName, "reports"),
			EventsDir:  filepath.Join(DefaultDirName, "events"),
		},
		Privacy: PrivacyConfig{
			NetworkAccess:           "disabled",
			IncludeFullDiffs:        false,
			IncludeUncommittedDiff:  false,
			IncludeAgentTranscripts: false,
			IncludeSourceContents:   false,
			RedactSecrets:           true,
		},
		Reports: ReportsConfig{
			Archive:      true,
			UpdateLatest: true,
			Defaults: ReportDefaults{
				ActivityBranch: "current",
				ComparisonBase: "default",
				Include: []string{
					"commits",
					"features",
					"branches",
					"tests",
					"build_evidence",
					"checkpoints",
				},
				AISummary: "optional",
			},
			Schedules: []ScheduleConfig{
				{
					Name:      "daily-activity",
					Enabled:   true,
					Frequency: "daily",
					At:        "18:00",
					Type:      "activity",
					Window:    "previous_calendar_day",
					CatchUp:   "run_once",
				},
				{
					Name:      "weekly-summary",
					Enabled:   true,
					Frequency: "weekly",
					Day:       "friday",
					At:        "17:00",
					Type:      "activity",
					Window:    "previous_calendar_week",
					CatchUp:   "run_once",
				},
			},
			Triggers: []TriggerConfig{
				{
					Name:            "main-merges",
					Enabled:         true,
					Event:           "merge",
					Target:          defaultBranch,
					ReportType:      "release",
					Mode:            "integration",
					DebounceSeconds: 30,
				},
				{
					Name:       "release-tags",
					Enabled:    true,
					Event:      "release_tag",
					ReportType: "release",
					Mode:       "versioned_release",
				},
			},
		},
		Output: OutputConfig{
			Formats:      []string{"html"},
			Archive:      true,
			UpdateLatest: true,
		},
	}
}

// FindDevTrailDir locates the .devtrail directory starting from startDir and walking upwards.
func FindDevTrailDir(startDir string) (string, error) {
	curr := startDir
	for {
		dtPath := filepath.Join(curr, DefaultDirName)
		if fi, err := os.Stat(dtPath); err == nil && fi.IsDir() {
			return dtPath, nil
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			break
		}
		curr = parent
	}
	return "", fmt.Errorf("devtrail is not initialized (no %s directory found)", DefaultDirName)
}

// LoadConfig loads and parses config.yaml from the repository root or .devtrail directory.
func LoadConfig(repoRoot string) (*Config, error) {
	cfgPath := filepath.Join(repoRoot, DefaultDirName, DefaultConfigFilename)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file at %s: %w", cfgPath, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid YAML syntax in %s: %w", cfgPath, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("configuration validation error: %w", err)
	}

	return &cfg, nil
}

// SaveConfig saves the configuration to the .devtrail directory.
func (c *Config) Save(repoRoot string) error {
	dir := filepath.Join(repoRoot, DefaultDirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	cfgPath := filepath.Join(dir, DefaultConfigFilename)
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	return os.WriteFile(cfgPath, data, 0644)
}

// Validate checks all configuration values for consistency and correctness.
func (c *Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("unsupported config version: %d (expected %d)", c.Version, CurrentVersion)
	}
	if strings.TrimSpace(c.Project.Name) == "" {
		return errors.New("project.name must not be empty")
	}
	if strings.TrimSpace(c.Project.DefaultBranch) == "" {
		return errors.New("project.default_branch must not be empty")
	}
	if strings.TrimSpace(c.Storage.Database) == "" {
		return errors.New("storage.database must not be empty")
	}
	if strings.TrimSpace(c.Storage.ReportsDir) == "" {
		return errors.New("storage.reports_dir must not be empty")
	}
	return nil
}

// RecommendedGitIgnoreEntries returns lines that should be included in .gitignore.
func RecommendedGitIgnoreEntries() []string {
	return []string{
		".devtrail/state.db",
		".devtrail/state.db-*",
		".devtrail/reports/",
		".devtrail/cache/",
		".devtrail/logs/",
		".devtrail/secrets/",
	}
}

// CheckGitIgnore examines the repo's .gitignore to see which entries are missing.
func CheckGitIgnore(repoRoot string) (missing []string, exists bool, err error) {
	giPath := filepath.Join(repoRoot, ".gitignore")
	data, err := os.ReadFile(giPath)
	if errors.Is(err, os.ErrNotExist) {
		return RecommendedGitIgnoreEntries(), false, nil
	} else if err != nil {
		return nil, false, err
	}

	content := string(data)
	lines := strings.Split(content, "\n")
	lineMap := make(map[string]bool)
	for _, l := range lines {
		lineMap[strings.TrimSpace(l)] = true
	}

	for _, rec := range RecommendedGitIgnoreEntries() {
		if !lineMap[rec] {
			missing = append(missing, rec)
		}
	}
	return missing, true, nil
}

// AppendGitIgnore appends missing entries to .gitignore.
func AppendGitIgnore(repoRoot string, entries []string) error {
	if len(entries) == 0 {
		return nil
	}
	giPath := filepath.Join(repoRoot, ".gitignore")
	f, err := os.OpenFile(giPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	builder := strings.Builder{}
	builder.WriteString("\n# DevTrail local data (private)\n")
	for _, e := range entries {
		builder.WriteString(e + "\n")
	}

	_, err = f.WriteString(builder.String())
	return err
}
