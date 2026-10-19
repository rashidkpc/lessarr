package decision

import (
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/probe"
)

type caseSpec struct {
	Name        string  `toml:"name"`
	Codec       string  `toml:"codec"`
	Width       int     `toml:"width"`
	Height      int     `toml:"height"`
	FPS         float64 `toml:"fps"`
	BitrateKbps int64   `toml:"bitrate_kbps"`
	SizeMB      int64   `toml:"size_mb"`
	DurationS   float64 `toml:"duration_s"`
	Container   string  `toml:"container"`

	BitDepth    int    `toml:"bit_depth"`
	FieldOrder  string `toml:"field_order"`
	HDR         string `toml:"hdr"`
	DoViProfile int    `toml:"dovi_profile"`
	Tagged      bool   `toml:"tagged"`

	Audio []audioSpec `toml:"audio"`
	Sub   []subSpec   `toml:"sub"`

	// library overrides
	Mode        string `toml:"mode"`
	Allow10Bit  bool   `toml:"allow_10bit"`
	MaxLongEdge int    `toml:"max_long_edge"`
	MinSizeMB   int64  `toml:"min_size_mb"`

	Action string   `toml:"action"`
	Codes  []string `toml:"codes"`
}

type audioSpec struct {
	Codec    string `toml:"codec"`
	Profile  string `toml:"profile"`
	Channels int    `toml:"channels"`
	Lang     string `toml:"lang"`
	Default  bool   `toml:"default"`
	Forced   bool   `toml:"forced"`
}

type subSpec struct {
	Codec   string `toml:"codec"`
	Profile string `toml:"profile"`
	Lang    string `toml:"lang"`
	Default bool   `toml:"default"`
	Forced  bool   `toml:"forced"`
}

func defaultPolicy() Policy {
	d := config.Default()
	return Policy{Audio: d.Audio, Subtitles: d.Subtitles}
}

func libraryFor(c caseSpec) config.Library {
	lib := config.DefaultLibrary()
	if c.Mode != "" {
		lib.Mode = c.Mode
	}
	if c.Allow10Bit {
		lib.Allow10Bit = true
	}
	if c.MaxLongEdge != 0 {
		lib.MaxLongEdge = c.MaxLongEdge
	}
	if c.MinSizeMB != 0 {
		lib.MinSizeMB = c.MinSizeMB
	}
	return lib
}

func mediaFor(c caseSpec) *probe.MediaInfo {
	depth := c.BitDepth
	if depth == 0 {
		depth = 8
	}
	field := c.FieldOrder
	if field == "" {
		field = "progressive"
	}
	info := &probe.MediaInfo{
		Path:         c.Name,
		Size:         c.SizeMB * config.MB,
		Duration:     c.DurationS,
		Container:    c.Container,
		VideoBitrate: c.BitrateKbps * 1000,
	}
	info.Video = probe.Video{
		Index:      0,
		Codec:      c.Codec,
		Width:      c.Width,
		Height:     c.Height,
		FPS:        c.FPS,
		BitDepth:   depth,
		FieldOrder: field,
	}
	switch c.HDR {
	case "hdr10":
		info.Video.HDR.HDR10 = true
	case "hlg":
		info.Video.HDR.HLG = true
	case "dovi":
		info.Video.HDR.DoVi = true
		info.Video.HDR.DoViProfile = c.DoViProfile
	}

	idx := 1
	for _, a := range c.Audio {
		info.Audio = append(info.Audio, probe.Audio{
			Index:    idx,
			Codec:    a.Codec,
			Profile:  a.Profile,
			Channels: a.Channels,
			Language: probe.NormalizeLang(a.Lang),
			Default:  a.Default,
			Forced:   a.Forced,
		})
		idx++
	}
	for _, s := range c.Sub {
		info.Subs = append(info.Subs, probe.Sub{
			Index:    idx,
			Codec:    s.Codec,
			Language: probe.NormalizeLang(s.Lang),
			Default:  s.Default,
			Forced:   s.Forced,
		})
		idx++
	}
	return info
}

func loadTable(t *testing.T) []caseSpec {
	t.Helper()
	var doc struct {
		Case []caseSpec `toml:"case"`
	}
	path := filepath.Join("..", "..", "testdata", "decisions.toml")
	if _, err := toml.DecodeFile(path, &doc); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return doc.Case
}

// TestDecisionTable is the regression net for the entire rule set. Each case is
// a hand-written probe fact set, so codecs we cannot synthesize are still
// covered, and the test needs no ffmpeg and no GPU.
func TestDecisionTable(t *testing.T) {
	cases := loadTable(t)
	if len(cases) < 30 {
		t.Fatalf("expected at least 30 cases, got %d", len(cases))
	}

	for _, c := range cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			info := mediaFor(c)
			got := Decide(info, libraryFor(c), defaultPolicy(), Options{Tagged: c.Tagged})

			if string(got.Action) != c.Action {
				t.Fatalf("action = %s, want %s\n  codes: %v\n  reasons: %v",
					got.Action, c.Action, got.Codes, got.Reasons)
			}
			for _, want := range c.Codes {
				if !got.Has(Code(want)) {
					t.Errorf("missing code %q; got %v", want, got.Codes)
				}
			}
			if len(got.Reasons) == 0 {
				t.Error("decision carries no human-readable reason")
			}

			switch got.Action {
			case Skip:
				if len(got.AudioKeep) > 0 || len(got.SubsKeep) > 0 || got.ScaleTo != nil || got.Deinterlace {
					t.Errorf("skip decision claims work: %+v", got)
				}
			case Transcode:
				if got.Deinterlace != info.Video.Interlaced() {
					t.Errorf("deinterlace = %v, want %v", got.Deinterlace, info.Video.Interlaced())
				}
				if got.EstimatedOutputBytes > info.Size {
					t.Errorf("projected output %d larger than source %d", got.EstimatedOutputBytes, info.Size)
				}
			case Remux:
				if got.Deinterlace || got.ScaleTo != nil {
					t.Errorf("remux must not touch video: %+v", got)
				}
				if got.EstimatedOutputBytes > info.Size {
					t.Errorf("projected output %d larger than source %d", got.EstimatedOutputBytes, info.Size)
				}
			}
		})
	}
}

// TestScaleCap pins the bug that a height-based cap upscales wide content:
// 1920x800 at a 1080p cap must not become 2592x1080.
func TestScaleCap(t *testing.T) {
	cases := []struct {
		name    string
		w, h    int
		cap     int
		wantNil bool
		wantW   int
		wantH   int
	}{
		{"wide 2.40:1 unchanged at 1080p cap", 1920, 800, 1920, true, 0, 0},
		{"4k 16:9 downscaled", 3840, 2160, 1920, false, 1920, 1080},
		{"4k 2.40:1 downscaled by long edge", 3840, 1600, 1920, false, 1920, 800},
		{"720p never upscaled", 1280, 720, 1920, true, 0, 0},
		{"no cap preserves source", 1920, 1080, 0, true, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info := &probe.MediaInfo{Video: probe.Video{Width: c.w, Height: c.h}}
			got := targetSize(info, c.cap)
			if c.wantNil {
				if got != nil {
					t.Fatalf("got %dx%d, want no scaling", got.W, got.H)
				}
				return
			}
			if got == nil {
				t.Fatal("expected scaling, got none")
			}
			if got.W != c.wantW || got.H != c.wantH {
				t.Errorf("got %dx%d, want %dx%d", got.W, got.H, c.wantW, c.wantH)
			}
			if got.W%2 != 0 || got.H%2 != 0 {
				t.Errorf("dimensions must be even, got %dx%d", got.W, got.H)
			}
		})
	}
}

// TestLosslessDropBeatsConversion documents the rule that a redundant lossless
// track is dropped rather than re-encoded when a kept lossy track already
// covers the same language.
func TestLosslessDropBeatsConversion(t *testing.T) {
	info := &probe.MediaInfo{
		Size:         8_000 * config.MB,
		Duration:     7200,
		VideoBitrate: 3_000_000,
		Video:        probe.Video{Codec: "hevc", Width: 1920, Height: 1080, FPS: 24, BitDepth: 8},
		Audio: []probe.Audio{
			{Index: 1, Codec: "truehd", Channels: 8, Language: "eng", Default: true},
			{Index: 2, Codec: "ac3", Channels: 6, Language: "eng"},
		},
	}
	got := Decide(info, config.DefaultLibrary(), defaultPolicy(), Options{})
	if !got.Has(CodeLosslessDrop) {
		t.Fatalf("expected lossless_drop, got %v", got.Codes)
	}
	if got.Has(CodeLosslessConvert) {
		t.Errorf("converted a redundant lossless track: %v", got.Codes)
	}
	if len(got.AudioTargets) != 0 {
		t.Errorf("expected no audio re-encode, got %v", got.AudioTargets)
	}
	if len(got.AudioKeep) != 1 || got.AudioKeep[0] != 2 {
		t.Errorf("expected only the AC3 track kept, got %v", got.AudioKeep)
	}
}

func TestAudioNeverEmptied(t *testing.T) {
	info := &probe.MediaInfo{
		Size:         2_000 * config.MB,
		Duration:     7200,
		VideoBitrate: 2_000_000,
		Video:        probe.Video{Codec: "hevc", Width: 1920, Height: 1080, FPS: 24, BitDepth: 8},
		Audio: []probe.Audio{
			{Index: 1, Codec: "ac3", Channels: 6, Language: "spa"},
			{Index: 2, Codec: "ac3", Channels: 6, Language: "deu"},
		},
	}
	got := Decide(info, config.DefaultLibrary(), defaultPolicy(), Options{})
	// Neither language is kept and no track is flagged default, so the first
	// track must survive rather than producing a silent file.
	if got.Action != Skip {
		if len(got.AudioKeep) == 0 {
			t.Fatal("all audio tracks would be dropped")
		}
	}
}

func TestPriorHardwareHEVCIsSkipped(t *testing.T) {
	info := &probe.MediaInfo{
		Size:         8_000 * config.MB,
		Duration:     7200,
		VideoBitrate: 12_000_000,
		Video: probe.Video{
			Codec: "hevc", Width: 1920, Height: 1080, FPS: 24, BitDepth: 8,
			EncoderTag: "Lavc59.37.100 hevc_vaapi",
		},
	}
	got := Decide(info, config.DefaultLibrary(), defaultPolicy(), Options{})
	if got.Action != Skip || !got.Has(CodeHardwareProvenance) {
		t.Fatalf("got %s %v, want skip for prior hardware encode", got.Action, got.Codes)
	}
}

func TestBPPWastefulDoesNotInventVideoSavings(t *testing.T) {
	info := &probe.MediaInfo{
		Size:         10_000 * config.MB,
		Duration:     7200,
		VideoBitrate: 12_000_000,
		Video:        probe.Video{Codec: "hevc", Width: 1920, Height: 1080, FPS: 24, BitDepth: 8},
	}
	got := Decide(info, config.DefaultLibrary(), defaultPolicy(), Options{})
	if got.Action != Transcode || !got.Has(CodeBPPWasteful) {
		t.Fatalf("got %s %v, want bpp_wasteful transcode", got.Action, got.Codes)
	}
	if !got.VideoSavingsUnmeasured {
		t.Fatal("already-modern codec transcode must be marked unmeasured")
	}
	if got.EstimatedSavings != 0 || got.EstimatedOutputBytes != info.Size {
		t.Errorf("must not invent video savings: savings=%v output=%d source=%d",
			got.EstimatedSavings, got.EstimatedOutputBytes, info.Size)
	}
}

func TestCodecGapKeepsModeledEstimate(t *testing.T) {
	info := &probe.MediaInfo{
		Size:         6_866 * config.MB,
		Duration:     7200,
		VideoBitrate: 8_000_000,
		Video:        probe.Video{Codec: "h264", Width: 1920, Height: 1080, FPS: 24, BitDepth: 8},
	}
	got := Decide(info, config.DefaultLibrary(), defaultPolicy(), Options{})
	if got.VideoSavingsUnmeasured {
		t.Fatal("codec-gap savings should retain its explicitly modeled estimate")
	}
	if got.EstimatedSavings < 0.49 || got.EstimatedSavings > 0.51 {
		t.Errorf("codec-gap estimate = %v, want approximately 50%%", got.EstimatedSavings)
	}
}

// TestDurationGateReplacesSizeGate documents why the sample filter is a runtime
// rather than a byte count: a 22-minute SD episode can be smaller than a
// two-minute trailer.
func TestDurationGate(t *testing.T) {
	base := func(duration float64, sizeMB int64) *probe.MediaInfo {
		return &probe.MediaInfo{
			Size:         sizeMB * config.MB,
			Duration:     duration,
			VideoBitrate: 6_000_000,
			Video:        probe.Video{Codec: "h264", Width: 1920, Height: 1080, FPS: 24, BitDepth: 8},
			Audio:        []probe.Audio{{Index: 1, Codec: "ac3", Channels: 6, Language: "eng", Default: true}},
		}
	}

	t.Run("short sample is skipped", func(t *testing.T) {
		got := Decide(base(40, 45), config.DefaultLibrary(), defaultPolicy(), Options{})
		if got.Action != Skip || !got.Has(CodeBelowMinDuration) {
			t.Errorf("got %s %v, want skip with %s", got.Action, got.Codes, CodeBelowMinDuration)
		}
	})

	t.Run("small but real episode is processed", func(t *testing.T) {
		// 180 MB over 22 minutes: too small for a film-sized byte gate, but a
		// perfectly ordinary episode.
		got := Decide(base(22*60, 180), config.DefaultLibrary(), defaultPolicy(), Options{})
		if got.Action != Transcode {
			t.Errorf("got %s %v, want transcode", got.Action, got.Codes)
		}
	})

	t.Run("unknown duration fails open", func(t *testing.T) {
		// A container that reports no runtime must not cause the file to be
		// silently ignored.
		got := Decide(base(0, 900), config.DefaultLibrary(), defaultPolicy(), Options{})
		if got.Has(CodeBelowMinDuration) {
			t.Errorf("unknown duration should not trip the gate: %v", got.Codes)
		}
	})
}
