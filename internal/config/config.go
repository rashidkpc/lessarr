// Package config loads and validates lessarr's TOML configuration.
//
// The file is the source of truth: every policy decision the engine makes is
// traceable to a value here. Unknown keys are rejected so that typos in a
// config don't silently fall back to defaults.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// MB is one mebibyte. Size settings in the config are expressed in MB so the
// numbers read the way people describe media files.
const (
	MB = int64(1 << 20)
	GB = MB * 1024
)

// Config is the root of the configuration file.
type Config struct {
	Server    Server    `toml:"server"`
	Encoder   Encoder   `toml:"encoder"`
	Audio     Audio     `toml:"audio"`
	Subtitles Subtitles `toml:"subtitles"`
	Trash     Trash     `toml:"trash"`
	Libraries []Library `toml:"library"`
}

type Trash struct {
	Retention Duration `toml:"retention"`
	MaxSizeGB int64    `toml:"max_size_gb"`
}

type Server struct {
	Listen   string `toml:"listen"`
	DB       string `toml:"db"`
	LogLevel string `toml:"log_level"`
}

type Encoder struct {
	Backend  string `toml:"backend"`   // auto | vaapi | qsv | software
	Device   string `toml:"device"`    // e.g. /dev/dri/renderD128
	Workers  int    `toml:"workers"`   // encoding slots; keep at 1 on weak hardware
	Nice     int    `toml:"nice"`      // process priority
	DecideBy string `toml:"decide_by"` // heuristic | sample
}

type Audio struct {
	KeepLanguages  []string `toml:"keep_languages"`
	KeepDefault    bool     `toml:"keep_default_track"`
	KeepOriginal   bool     `toml:"keep_original_language"`
	LosslessToEAC3 bool     `toml:"lossless_to_eac3"`
	EAC3Bitrate51  string   `toml:"eac3_bitrate_51"`
	EAC3Bitrate20  string   `toml:"eac3_bitrate_20"`
}

type Subtitles struct {
	KeepLanguages []string `toml:"keep_languages"`
	KeepForced    bool     `toml:"keep_forced"`
}

// Library is one watched location with its own policy.
type Library struct {
	Name string `toml:"name"`
	Path string `toml:"path"`
	// Write gates the only step that modifies a file. It is false by default,
	// and it is per library because getting started is staged: enable one,
	// watch it, then enable the rest. Stopping everything at once is a runtime
	// action (`lessarr pause`), not a config flag that can be left behind.
	Write bool   `toml:"write"`
	Mode  string `toml:"mode"` // transcode | remux | monitor

	MaxLongEdge int `toml:"max_long_edge"` // 0 preserves source resolution; never upscales
	MinLongEdge int `toml:"min_long_edge"` // skip anything smaller

	ICQ    int    `toml:"icq"`
	Preset string `toml:"preset"`

	// Validation is "sample" (short decodes across the file) or "full"
	// (decode every frame). Sampling is the default because a full decode of a
	// feature-length title costs many minutes.
	Validation string `toml:"validation"`

	MinSavingsPercent float64 `toml:"min_savings_percent"`
	MinRemuxBytes     int64   `toml:"min_remux_bytes"`
	// MinDuration is the real sample filter. A byte count is a poor proxy for
	// "too short to be a title": a two-minute trailer can be 300 MB and a
	// 22-minute SD episode can be 180 MB.
	//
	// MinSizeMB answers a different question — whether reclaiming anything is
	// worth minutes of GPU time — and is deliberately not a sample filter.
	MinDuration Duration `toml:"min_duration"`
	MinSizeMB   int64    `toml:"min_size_mb"`

	SkipIfNewerThan Duration `toml:"skip_if_newer_than"`
	HDR             string   `toml:"hdr"`
	FreeSpaceFactor float64  `toml:"free_space_factor"`
	Allow10Bit      bool     `toml:"allow_10bit"`
	Ignore          []string `toml:"ignore"`
}

// Duration is a time.Duration that decodes from strings like "10m".
type Duration time.Duration

func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(strings.TrimSpace(string(text)))
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// Std converts back to a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Default returns a fully populated config. Fields are set before decoding so
// that any key absent from the file keeps its default.
func Default() Config {
	return Config{
		Server: Server{
			Listen:   "0.0.0.0:8585",
			DB:       "/config/lessarr.db",
			LogLevel: "info",
		},
		Encoder: Encoder{
			Backend: "auto",
			Device:  "/dev/dri/renderD128",
			Workers: 1,
			Nice:    15,
			// Sampling lands in M1; until then the heuristic is the honest
			// default rather than pretending to measure.
			DecideBy: "heuristic",
		},
		Audio: Audio{
			KeepLanguages:  []string{"eng"},
			KeepDefault:    true,
			KeepOriginal:   true,
			LosslessToEAC3: true,
			EAC3Bitrate51:  "640k",
			EAC3Bitrate20:  "256k",
		},
		Subtitles: Subtitles{
			KeepLanguages: []string{"eng", "fre"},
			KeepForced:    true,
		},
		Trash: Trash{
			Retention: Duration(48 * time.Hour),
			MaxSizeGB: 20,
		},
	}
}

// DefaultLibrary returns a library policy with all defaults applied and no
// path, for commands that run without a config file (probe, for instance).
func DefaultLibrary() Library {
	var l Library
	l.applyDefaults()
	return l
}

// Load reads, decodes, normalises and validates a config file.
func Load(path string) (*Config, error) {
	cfg := Default()
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("%s: unknown keys: %s", path, strings.Join(keys, ", "))
	}
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

func (c *Config) normalize() {
	for i := range c.Libraries {
		c.Libraries[i].applyDefaults()
	}
}

func (l *Library) applyDefaults() {
	if l.Mode == "" {
		l.Mode = "transcode"
	}
	// MaxLongEdge deliberately keeps 0: it means "preserve source resolution".
	if l.MinLongEdge == 0 {
		l.MinLongEdge = 480
	}
	if l.ICQ == 0 {
		l.ICQ = 24
	}
	if l.Preset == "" {
		l.Preset = "slow"
	}
	if l.Validation == "" {
		l.Validation = "sample"
	}
	if l.MinSavingsPercent == 0 {
		l.MinSavingsPercent = 25
	}
	if l.MinRemuxBytes == 0 {
		l.MinRemuxBytes = 100
	}
	if l.MinDuration == 0 {
		l.MinDuration = Duration(3 * time.Minute)
	}
	if l.MinSizeMB == 0 {
		l.MinSizeMB = 50
	}
	if l.SkipIfNewerThan == 0 {
		l.SkipIfNewerThan = Duration(10 * time.Minute)
	}
	if l.HDR == "" {
		l.HDR = "skip"
	}
	if l.FreeSpaceFactor == 0 {
		l.FreeSpaceFactor = 1.15
	}
}

// Validate checks the parts of the config that can be wrong independently of
// any media file.
func (c *Config) Validate() error {
	switch c.Encoder.Backend {
	case "auto", "vaapi", "qsv", "software":
	default:
		return fmt.Errorf("encoder.backend: unknown value %q", c.Encoder.Backend)
	}
	switch c.Encoder.DecideBy {
	case "heuristic", "sample":
	default:
		return fmt.Errorf("encoder.decide_by: unknown value %q", c.Encoder.DecideBy)
	}
	if c.Encoder.Workers < 1 {
		return fmt.Errorf("encoder.workers: must be >= 1")
	}
	if c.Trash.Retention < 0 {
		return fmt.Errorf("trash.retention: must be >= 0")
	}
	if c.Trash.MaxSizeGB < 0 {
		return fmt.Errorf("trash.max_size_gb: must be >= 0")
	}

	if len(c.Libraries) == 0 {
		return fmt.Errorf("no [[library]] sections defined")
	}
	seen := map[string]bool{}
	for i := range c.Libraries {
		l := &c.Libraries[i]
		where := fmt.Sprintf("library[%d]", i)
		if l.Name == "" {
			return fmt.Errorf("%s: name is required", where)
		}
		if seen[l.Name] {
			return fmt.Errorf("%s: duplicate library name %q", where, l.Name)
		}
		seen[l.Name] = true
		if l.Path == "" {
			return fmt.Errorf("library %q: path is required", l.Name)
		}
		switch l.Mode {
		case "transcode", "remux", "monitor":
		default:
			return fmt.Errorf("library %q: mode must be transcode, remux or monitor", l.Name)
		}
		if l.ICQ < 1 || l.ICQ > 51 {
			return fmt.Errorf("library %q: icq must be 1..51", l.Name)
		}
		switch l.Validation {
		case "sample", "full":
		default:
			return fmt.Errorf("library %q: validation must be sample or full", l.Name)
		}
		if l.MinLongEdge < 0 {
			return fmt.Errorf("library %q: min_long_edge must be >= 0", l.Name)
		}
		if l.MinDuration < 0 {
			return fmt.Errorf("library %q: min_duration must be >= 0", l.Name)
		}
		if l.MaxLongEdge > 0 && l.MaxLongEdge < l.MinLongEdge {
			return fmt.Errorf("library %q: max_long_edge must be 0 or >= min_long_edge", l.Name)
		}
		if l.MinSavingsPercent < 1 || l.MinSavingsPercent > 95 {
			return fmt.Errorf("library %q: min_savings_percent must be 1..95", l.Name)
		}
		if l.FreeSpaceFactor < 1 {
			return fmt.Errorf("library %q: free_space_factor must be >= 1", l.Name)
		}
		switch l.HDR {
		case "skip", "passthrough":
		default:
			return fmt.Errorf("library %q: hdr must be skip or passthrough", l.Name)
		}
	}
	return nil
}

// LibraryFor returns the library whose path contains the given file, preferring
// the longest match so nested libraries work.
// LibraryByName resolves a queued job's library after a restart.
func (c *Config) LibraryByName(name string) (Library, bool) {
	for _, l := range c.Libraries {
		if l.Name == name {
			return l, true
		}
	}
	return Library{}, false
}

func (c *Config) LibraryFor(path string) (Library, bool) {
	var best Library
	found := false
	for _, l := range c.Libraries {
		if strings.HasPrefix(path, l.Path) && (!found || len(l.Path) > len(best.Path)) {
			best, found = l, true
		}
	}
	return best, found
}

// Find resolves the config path, falling back to conventional names in the
// working directory.
func Find(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	for _, name := range []string{"config.toml", "lessarr.toml"} {
		if _, err := os.Stat(name); err == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("no config file found (pass --config)")
}
