package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestEmbeddedDefaultsParse is the most important test in this package: it
// catches the case where someone edits default.yaml in a way the loader can't
// parse. The embedded defaults must always be loadable.
func TestEmbeddedDefaultsParse(t *testing.T) {
	cfg, err := Load("/this/file/does/not/exist")
	if err != nil {
		t.Fatalf("Load with no user file should succeed using embedded defaults, got: %v", err)
	}

	// Spot-check a handful of fields to make sure the YAML actually populated
	// the struct rather than leaving everything zero.
	if cfg.Stash.URL == "" {
		t.Error("expected Stash.URL to be populated from defaults")
	}
	if cfg.Scan.PhashDistance != 4 {
		t.Errorf("expected default phash_distance=4, got %d", cfg.Scan.PhashDistance)
	}
	if cfg.Scan.DurationDiffSeconds != 1.0 {
		t.Errorf("expected default duration_diff_seconds=1.0, got %v", cfg.Scan.DurationDiffSeconds)
	}
	if got, want := cfg.Quarantine.Dir, "/data/_quarantine"; got != want {
		t.Errorf("expected default quarantine.dir=%q, got %q", want, got)
	}
	if got, want := cfg.Quarantine.Tag, "_custodian_quarantine"; got != want {
		t.Errorf("expected default quarantine.tag=%q, got %q", want, got)
	}
	if !cfg.Sidecars.Enabled {
		t.Error("expected sidecars.enabled to default to true")
	}
	if len(cfg.Organize.RequiredFields) == 0 {
		t.Error("expected organize.required_fields to be populated by defaults")
	}
	if got, want := cfg.Quarantine.MistagTag, "_custodian_mistag"; got != want {
		t.Errorf("expected default quarantine.mistag_tag=%q, got %q", want, got)
	}
	if cfg.Sources.StashBoxEndpoint != "" {
		t.Errorf("expected default sources.stash_box_endpoint to be empty, got %q", cfg.Sources.StashBoxEndpoint)
	}
	if cfg.Sources.FSS.DB != "" || cfg.Sources.FSS.JSONDir != "" {
		t.Errorf("expected default sources.fss to be empty, got %+v", cfg.Sources.FSS)
	}
}

// TestUserOverridesMergeOverDefaults verifies that user values replace
// defaults at the field level. yaml.v3 merges struct fields key-by-key, so a
// user file that only specifies stash.url should still inherit every other
// field from defaults.
func TestUserOverridesMergeOverDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	userYAML := `
stash:
  url: http://example.test:9999
scan:
  phash_distance: 8
`
	if err := os.WriteFile(path, []byte(userYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Stash.URL, "http://example.test:9999"; got != want {
		t.Errorf("Stash.URL = %q, want %q", got, want)
	}
	if got, want := cfg.Scan.PhashDistance, 8; got != want {
		t.Errorf("Scan.PhashDistance = %d, want %d", got, want)
	}
	// Unspecified field — must come from defaults.
	if got, want := cfg.Scan.DurationDiffSeconds, 1.0; got != want {
		t.Errorf("Scan.DurationDiffSeconds = %v, want %v (default)", got, want)
	}
	// Unspecified slice — must come from defaults.
	if len(cfg.Sidecars.Extensions) == 0 {
		t.Error("Sidecars.Extensions should still be populated from defaults")
	}
}

// TestUserSliceReplacesWholesale documents (and locks in) the yaml.v3
// behavior that user-provided slices REPLACE defaults rather than appending.
// This is the desired "I want exactly this list" semantics.
func TestUserSliceReplacesWholesale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	userYAML := `
stash:
  url: http://example.test:9999
sidecars:
  extensions:
    - srt
`
	if err := os.WriteFile(path, []byte(userYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Sidecars.Extensions, []string{"srt"}; len(got) != len(want) || got[0] != want[0] {
		t.Errorf("Sidecars.Extensions = %v, want %v (user list should replace, not append)", got, want)
	}
}

// TestValidateRejectsEmptyQuarantineDir makes sure a blanked-out quarantine
// directory is caught at load time: apply moves every loser there, and an
// empty value would send moveFiles at the library root.
func TestValidateRejectsEmptyQuarantineDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	userYAML := `
stash:
  url: http://example.test:9999
quarantine:
  dir: ""
`
	if err := os.WriteFile(path, []byte(userYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for an empty quarantine.dir, got nil")
	}
	if !strings.Contains(err.Error(), "quarantine.dir") {
		t.Errorf("expected error to mention quarantine.dir, got: %v", err)
	}
}

// A config written for the old spine still parses — yaml.v3 drops keys with
// no struct field — so `config validate` is the only place a retired key can
// be reported. Every message has to name what replaced it, or the user is
// told what not to do without being told what to do.
func TestCheckReportsRetiredKeysAndTheirReplacements(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	userYAML := `
stash:
  url: http://example.test:9999
scoring:
  path_priority:
    - /sorted
apply:
  scenes:
    default_action: tag
merge:
  history:
    play_history: true
`
	if err := os.WriteFile(path, []byte(userYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	problems, err := Check(path)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	got := map[string]string{}
	for _, p := range problems {
		got[p.Key] = p.Detail
	}
	for _, key := range []string{"scoring", "apply", "merge"} {
		detail, ok := got[key]
		if !ok {
			t.Errorf("%s not reported as retired; got %v", key, problems)
			continue
		}
		if !strings.Contains(detail, "retired") {
			t.Errorf("%s: detail %q does not say it is retired", key, detail)
		}
	}
	if d := got["scoring"]; !strings.Contains(d, "keeper rules") {
		t.Errorf("scoring: detail should name the keeper rules as the replacement, got %q", d)
	}
	if d := got["merge"]; !strings.Contains(d, "stash-go") {
		t.Errorf("merge: detail should name stash-go's union policy, got %q", d)
	}
	if d := got["apply"]; !strings.Contains(d, "--commit") {
		t.Errorf("apply: detail should describe what apply does now, got %q", d)
	}
}

// A config in the current shape is clean, and so is having no file at all.
func TestCheckAcceptsTheCurrentShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	problems, err := Check(path)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(problems) != 0 {
		t.Errorf("the file `config init` writes must validate clean, got %v", problems)
	}

	problems, err = Check(filepath.Join(dir, "nothing-here.yaml"))
	if err != nil {
		t.Fatalf("Check (missing file): %v", err)
	}
	if len(problems) != 0 {
		t.Errorf("a missing config is the embedded defaults, got %v", problems)
	}
}

// TestStashAPIKey verifies the env-var indirection. We never want the API
// key serialized into the config struct.
func TestStashAPIKey(t *testing.T) {
	t.Setenv("SFX_TEST_STASH_KEY", "abc123")
	cfg := &Config{Stash: Stash{APIKeyEnv: "SFX_TEST_STASH_KEY"}}
	if got, want := cfg.StashAPIKey(), "abc123"; got != want {
		t.Errorf("StashAPIKey() = %q, want %q", got, want)
	}

	cfg.Stash.APIKeyEnv = ""
	if got := cfg.StashAPIKey(); got != "" {
		t.Errorf("StashAPIKey() with empty APIKeyEnv = %q, want empty", got)
	}

	cfg.Stash.APIKeyEnv = "SFX_TEST_NONEXISTENT_KEY"
	if got := cfg.StashAPIKey(); got != "" {
		t.Errorf("StashAPIKey() with unset env var = %q, want empty", got)
	}
}

// TestWriteDefaultRefusesOverwrite makes sure `custodian config init` cannot
// silently clobber an existing config.
func TestWriteDefaultRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("# existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteDefault(path); err == nil {
		t.Fatal("expected WriteDefault to refuse overwriting an existing file")
	}
}

func TestWriteDefaultCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := WriteDefault(path); err != nil {
		t.Fatalf("WriteDefault: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The file should be byte-identical to the embedded YAML, comments and all.
	if string(data) != string(DefaultYAML()) {
		t.Error("written file does not match embedded default YAML")
	}
}

// Load must report a user file that fails to parse, not silently fall back
// to defaults.
func TestLoadRejectsMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("stash: [this is not valid: yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error for malformed YAML, got nil")
	}
	if !strings.Contains(err.Error(), "parsing") {
		t.Errorf("expected a parsing error, got: %v", err)
	}
}

// A read failure other than "not exist" — e.g. path is a directory — must be
// reported rather than silently treated as a missing file.
func TestLoadReportsReadErrorsOtherThanNotExist(t *testing.T) {
	dir := t.TempDir() // a directory, not a file — os.ReadFile refuses it.
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected an error reading a directory as the config file, got nil")
	}
	if !strings.Contains(err.Error(), "reading") {
		t.Errorf("expected a reading error, got: %v", err)
	}
}

func TestValidateChecksEachField(t *testing.T) {
	base := func() Config {
		return Config{
			Stash:      Stash{URL: "http://example.test"},
			Quarantine: Quarantine{Dir: "/data/_q", Tag: "_q"},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"empty stash url", func(c *Config) { c.Stash.URL = "" }, "stash.url"},
		{"negative phash distance", func(c *Config) { c.Scan.PhashDistance = -1 }, "scan.phash_distance"},
		{"negative duration diff", func(c *Config) { c.Scan.DurationDiffSeconds = -1 }, "scan.duration_diff_seconds"},
		{"empty quarantine dir", func(c *Config) { c.Quarantine.Dir = "" }, "quarantine.dir"},
		{"empty quarantine tag", func(c *Config) { c.Quarantine.Tag = "" }, "quarantine.tag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected a validation error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), tt.wantErr)
			}
		})
	}

	valid := base()
	if err := valid.Validate(); err != nil {
		t.Errorf("expected the base config to validate clean, got: %v", err)
	}
}

func TestProblemString(t *testing.T) {
	p := Problem{Key: "scoring", Detail: "retired — see the keeper rules"}
	if got, want := p.String(), "scoring: retired — see the keeper rules"; got != want {
		t.Errorf("Problem.String() = %q, want %q", got, want)
	}
}

// Check must report a read failure other than "not exist" rather than
// treating it as a missing (and therefore valid) file.
func TestCheckReportsReadErrorsOtherThanNotExist(t *testing.T) {
	dir := t.TempDir()
	_, err := Check(dir) // a directory, not a file
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}

// A config file that fails to parse is Load's problem to report — Check
// surfaces it as a "config" problem rather than erroring itself.
func TestCheckReportsUnparsableConfigAsAProblem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("stash: [not valid yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	problems, err := Check(path)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(problems) != 1 || problems[0].Key != "config" {
		t.Errorf("expected a single config problem, got %v", problems)
	}
}

// A config that parses but fails Validate is also reported as a "config"
// problem.
func TestCheckReportsValidationFailureAsAProblem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	userYAML := "stash:\n  url: http://example.test\nquarantine:\n  dir: \"\"\n"
	if err := os.WriteFile(path, []byte(userYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	problems, err := Check(path)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(problems) != 1 || problems[0].Key != "config" {
		t.Errorf("expected a single config problem, got %v", problems)
	}
	if !strings.Contains(problems[0].Detail, "quarantine.dir") {
		t.Errorf("expected the problem to mention quarantine.dir, got %q", problems[0].Detail)
	}
}

// retiredIn must not choke on a file that doesn't even parse as YAML — that
// is Load's error to report, not retiredIn's.
func TestRetiredInIgnoresUnparsableInput(t *testing.T) {
	got := retiredIn([]byte("not: [valid: yaml"))
	if got != nil {
		t.Errorf("retiredIn on unparsable input = %v, want nil", got)
	}
}

func TestMarshalYAMLRoundTrips(t *testing.T) {
	cfg, err := Load("/this/file/does/not/exist")
	if err != nil {
		t.Fatal(err)
	}
	out, err := MarshalYAML(cfg)
	if err != nil {
		t.Fatalf("MarshalYAML: %v", err)
	}
	if !strings.Contains(string(out), "url:") {
		t.Errorf("expected marshaled YAML to contain stash.url, got:\n%s", out)
	}

	roundTripped := &Config{}
	if err := yaml.Unmarshal(out, roundTripped); err != nil {
		t.Fatalf("marshaled YAML does not parse: %v", err)
	}
	if roundTripped.Stash.URL != cfg.Stash.URL {
		t.Errorf("round-tripped Stash.URL = %q, want %q", roundTripped.Stash.URL, cfg.Stash.URL)
	}
}
