package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := writeTemp(t, `
[[library]]
name = "Movies"
path = "/media/movies"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Encoder.Workers != 1 {
		t.Errorf("workers = %d, want 1", cfg.Encoder.Workers)
	}
	l := cfg.Libraries[0]
	if l.Mode != "transcode" || l.ICQ != 24 || l.Preset != "slow" {
		t.Errorf("library defaults not applied: %+v", l)
	}
	if l.MinLongEdge != 480 || l.MinSizeMB != 50 || l.MinRemuxBytes != 100 {
		t.Errorf("size defaults not applied: %+v", l)
	}
	if l.MinDuration.Std() != 3*time.Minute {
		t.Errorf("min_duration = %v, want 3m", l.MinDuration.Std())
	}
	// The write switch must be off unless the word is typed.
	if l.Write {
		t.Error("write must default to false")
	}
	if l.SkipIfNewerThan.Std() != 10*time.Minute {
		t.Errorf("skip_if_newer_than = %v, want 10m", l.SkipIfNewerThan.Std())
	}
	if l.HDR != "skip" {
		t.Errorf("hdr = %q, want skip", l.HDR)
	}
	// Preserving resolution is the default, so a zero cap must survive.
	if l.MaxLongEdge != 0 {
		t.Errorf("max_long_edge = %d, want 0 (preserve source)", l.MaxLongEdge)
	}
	if l.Allow10Bit {
		t.Error("allow_10bit must default to false")
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	path := writeTemp(t, `
[encoder]
werkres = 4

[[library]]
name = "Movies"
path = "/media/movies"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error for an unknown key")
	}
	if !strings.Contains(err.Error(), "werkres") {
		t.Errorf("error should name the offending key, got: %v", err)
	}
}

func TestLoadRejectsBadLibrary(t *testing.T) {
	cases := map[string]string{
		"missing path": `
[[library]]
name = "Movies"
`,
		"missing name": `
[[library]]
path = "/media/movies"
`,
		"bad mode": `
[[library]]
name = "Movies"
path = "/media/movies"
mode = "transcod"
`,
		"bad icq": `
[[library]]
name = "Movies"
path = "/media/movies"
icq = 99
`,
		"duplicate names": `
[[library]]
name = "Movies"
path = "/a"

[[library]]
name = "Movies"
path = "/b"
`,
		"cap below floor": `
[[library]]
name = "Movies"
path = "/media/movies"
min_long_edge = 1080
max_long_edge = 720
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeTemp(t, body)); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestLoadRejectsNonLibraryConfig(t *testing.T) {
	_, err := Load(writeTemp(t, `[server]
listen = "0.0.0.0:1234"
`))
	if err == nil {
		t.Fatal("a config with no libraries is useless and should be rejected")
	}
}

func TestLibraryForPrefersLongestMatch(t *testing.T) {
	cfg := Default()
	cfg.Libraries = []Library{
		{Name: "Media", Path: "/media"},
		{Name: "Movies", Path: "/media/movies"},
	}
	got, ok := cfg.LibraryFor("/media/movies/Example.mkv")
	if !ok || got.Name != "Movies" {
		t.Errorf("LibraryFor = %q,%v, want Movies", got.Name, ok)
	}
	if _, ok := cfg.LibraryFor("/other/file.mkv"); ok {
		t.Error("expected no match for an unrelated path")
	}
}

func TestDurationParsing(t *testing.T) {
	path := writeTemp(t, `
[[library]]
name = "Movies"
path = "/media/movies"
skip_if_newer_than = "90s"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Libraries[0].SkipIfNewerThan.Std() != 90*time.Second {
		t.Errorf("got %v, want 90s", cfg.Libraries[0].SkipIfNewerThan.Std())
	}
}

func TestWriteSwitchIsOptIn(t *testing.T) {
	path := writeTemp(t, `
[[library]]
name = "Movies"
path = "/media/movies"
write = true
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Libraries[0].Write {
		t.Error("write = true was not honoured")
	}

	// And the old global switch is gone: silence is the safe state.
	if _, err := Load(writeTemp(t, `
[server]
dry_run = false

[[library]]
name = "Movies"
path = "/media/movies"
`)); err == nil {
		t.Error("server.dry_run should be rejected now that the switch is per library")
	}
}

func TestMinDurationOverride(t *testing.T) {
	path := writeTemp(t, `
[[library]]
name = "Movies"
path = "/media/movies"
min_duration = "10m"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Libraries[0].MinDuration.Std(); got != 10*time.Minute {
		t.Errorf("min_duration = %v, want 10m", got)
	}
}

func TestValidationDefaultsToSample(t *testing.T) {
	if got := DefaultLibrary().Validation; got != "sample" {
		t.Fatalf("default validation = %q, want sample", got)
	}
	cfg, err := Load(writeTemp(t, `[encoder]
backend = "software"

[[library]]
name = "TV"
path = "/media/tv"
validation = "full"
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Libraries[0].Validation != "full" {
		t.Fatalf("validation = %q, want full", cfg.Libraries[0].Validation)
	}
}
