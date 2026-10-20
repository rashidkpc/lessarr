// Package probe turns ffprobe output into the facts the decision engine needs.
//
// Two things here are deliberately more careful than a naive wrapper:
//
//   - MKV files frequently carry no per-stream bitrate, so video bitrate is
//     derived from the container total minus estimated audio, or sampled from
//     real packets when accuracy matters.
//   - HDR is detected from both color transfer characteristics and Dolby
//     Vision side data, because the policy treats those files differently.
package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Prober shells out to ffprobe.
type Prober struct {
	FFProbe string
}

func New() Prober { return Prober{FFProbe: "ffprobe"} }

// MediaInfo is the normalized description of one file.
type MediaInfo struct {
	Path           string
	Size           int64
	Duration       float64 // seconds
	Container      string
	VideoCount     int
	Video          Video
	Audio          []Audio
	Subs           []Sub
	VideoBitrate   int64 // bits per second
	BitrateSampled bool
	Tags           map[string]string
}

type Video struct {
	Index      int     `json:"index"`
	Codec      string  `json:"codec"`
	Profile    string  `json:"profile,omitempty"`
	EncoderTag string  `json:"encoder_tag,omitempty"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	Duration   float64 `json:"duration_seconds,omitempty"`
	FPS        float64 `json:"fps"`
	PixFmt     string  `json:"pix_fmt"`
	BitDepth   int     `json:"bit_depth"`
	FieldOrder string  `json:"field_order"`
	SAR        string  `json:"sar,omitempty"`
	HDR        HDR     `json:"hdr"`
}

// HardwareEncoded reports whether ffprobe's video-stream encoder tag identifies
// a prior Intel QSV/VAAPI HEVC encode. It is a strong signal that another lossy
// hardware encode would be a subsequent generation, not a codec upgrade.
func (v Video) HardwareEncoded() bool {
	if v.Codec != "hevc" {
		return false
	}
	tag := strings.ToLower(v.EncoderTag)
	return strings.Contains(tag, "hevc_vaapi") || strings.Contains(tag, "hevc_qsv")
}

type HDR struct {
	HDR10       bool `json:"hdr10"`
	HLG         bool `json:"hlg"`
	DoVi        bool `json:"dovi"`
	DoViProfile int  `json:"dovi_profile,omitempty"`
}

func (h HDR) Any() bool { return h.HDR10 || h.HLG || h.DoVi }

type Audio struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Profile  string `json:"profile,omitempty"`
	Channels int    `json:"channels"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Default  bool   `json:"default"`
	Forced   bool   `json:"forced"`
	Bitrate  int64  `json:"bitrate,omitempty"`
}

type Sub struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Default  bool   `json:"default"`
	Forced   bool   `json:"forced"`
}

// Image reports whether the subtitle format is bitmap-based (PGS, VobSub).
func (s Sub) Image() bool {
	switch s.Codec {
	case "hdmv_pgs_subtitle", "dvd_subtitle", "dvb_subtitle", "xsub":
		return true
	}
	return false
}

// Lossless reports whether an audio codec carries lossless audio.
//
// DTS is the awkward one: ffprobe reports codec "dts" for the whole family and
// distinguishes Master Audio by profile, so HRA (lossy) must not match.
func (a Audio) Lossless() bool {
	switch a.Codec {
	case "truehd", "mlp", "flac", "alac", "wavpack", "tta":
		return true
	}
	if strings.HasPrefix(a.Codec, "pcm_") {
		return true
	}
	if a.Codec == "dts" {
		p := strings.ToUpper(a.Profile)
		return strings.Contains(p, "MA") || strings.Contains(p, "LOSSLESS")
	}
	return false
}

// BitRate returns the stream bitrate, or a codec-typical estimate when the
// container does not record one.
func (a Audio) BitRate() int64 {
	if a.Bitrate > 0 {
		return a.Bitrate
	}
	switch a.Codec {
	case "truehd":
		return 3_000_000
	case "dts":
		if strings.Contains(strings.ToUpper(a.Profile), "MA") {
			return 4_000_000
		}
		return 1_509_000
	case "ac3", "eac3":
		return 640_000
	case "aac":
		return 192_000
	case "opus":
		return 128_000
	case "mp3":
		return 192_000
	case "flac", "alac":
		switch a.Channels {
		case 1:
			return 240_000
		case 2:
			return 900_000
		case 6:
			return 2_500_000
		case 8:
			return 3_500_000
		}
		return 900_000
	case "vorbis":
		return 160_000
	case "pcm_s16le":
		return 1_536_000
	case "pcm_s24le":
		return 2_304_000
	}
	return 256_000
}

// MaxLongEdge is the larger pixel dimension. Resolution policy caps this, not
// the height, so that 2.40:1 content is not upscaled.
func (m *MediaInfo) MaxLongEdge() int { return max(m.Video.Width, m.Video.Height) }

// BPP is bits per pixel per frame: the resolution-independent efficiency
// measure the engine thresholds on.
func (m *MediaInfo) BPP() float64 {
	if m.Video.Width == 0 || m.Video.Height == 0 || m.Video.FPS == 0 || m.VideoBitrate == 0 {
		return 0
	}
	return float64(m.VideoBitrate) / (float64(m.Video.Width) * float64(m.Video.Height) * m.Video.FPS)
}

// Interlaced reports combed content, which is always worth deinterlacing.
func (v Video) Interlaced() bool {
	switch v.FieldOrder {
	case "tt", "bb", "tb", "bt":
		return true
	}
	return false
}

// IsMatroska reports an MKV/WebM container.
func (m *MediaInfo) IsMatroska() bool { return strings.Contains(m.Container, "matroska") }

type ffprobeOutput struct {
	Format  ffFormat   `json:"format"`
	Streams []ffStream `json:"streams"`
}

type ffFormat struct {
	FormatName string            `json:"format_name"`
	Duration   string            `json:"duration"`
	Size       string            `json:"size"`
	BitRate    string            `json:"bit_rate"`
	Tags       map[string]string `json:"tags"`
}

type ffStream struct {
	Index             int               `json:"index"`
	CodecName         string            `json:"codec_name"`
	CodecType         string            `json:"codec_type"`
	Duration          string            `json:"duration"`
	Profile           string            `json:"profile"`
	Width             int               `json:"width"`
	Height            int               `json:"height"`
	AvgFrameRate      string            `json:"avg_frame_rate"`
	RFrameRate        string            `json:"r_frame_rate"`
	PixFmt            string            `json:"pix_fmt"`
	BitsPerRawSample  string            `json:"bits_per_raw_sample"`
	FieldOrder        string            `json:"field_order"`
	SampleAspectRatio string            `json:"sample_aspect_ratio"`
	ColorTransfer     string            `json:"color_transfer"`
	Channels          int               `json:"channels"`
	BitRate           string            `json:"bit_rate"`
	CodecTagString    string            `json:"codec_tag_string"`
	Tags              map[string]string `json:"tags"`
	Disposition       map[string]int    `json:"disposition"`
	SideDataList      []ffSideData      `json:"side_data_list"`
}

type ffSideData struct {
	SideDataType  string `json:"side_data_type"`
	DVProfile     int    `json:"dv_profile"`
	DVLevel       int    `json:"dv_level"`
	ELPresentFlag int    `json:"el_present_flag"`
}

// Probe runs ffprobe and normalizes the result.
func (p Prober) Probe(ctx context.Context, path string) (*MediaInfo, error) {
	cmd := exec.CommandContext(ctx, p.bin(),
		"-v", "error",
		"-print_format", "json",
		"-show_format", "-show_streams",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("ffprobe %s: %s", path, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("ffprobe %s: %w", path, err)
	}

	var raw ffprobeOutput
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("ffprobe %s: decode: %w", path, err)
	}
	return normalize(path, &raw)
}

// parseJSON is the pure half of Probe, split out so it can be tested against
// recorded ffprobe output.
func parseJSON(path string, data []byte) (*MediaInfo, error) {
	var raw ffprobeOutput
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode ffprobe output for %s: %w", path, err)
	}
	return normalize(path, &raw)
}

func normalize(path string, raw *ffprobeOutput) (*MediaInfo, error) {
	info := &MediaInfo{
		Path:      path,
		Container: raw.Format.FormatName,
		Duration:  parseFloat(raw.Format.Duration),
		Size:      parseInt(raw.Format.Size),
		Tags:      raw.Format.Tags,
	}
	if info.Duration == 0 {
		// Some streams only expose duration per-stream.
		for _, s := range raw.Streams {
			if d := parseFloat(s.Tags["DURATION"]); d > 0 {
				info.Duration = d
				break
			}
		}
	}
	if st, err := os.Stat(path); err == nil && info.Size == 0 {
		info.Size = st.Size()
	}

	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			info.VideoCount++
			if info.Video.Codec != "" {
				continue // first video stream is the feature
			}
			info.Video = Video{
				Index:      s.Index,
				Codec:      s.CodecName,
				Profile:    s.Profile,
				EncoderTag: tagValue(s.Tags, "encoder"),
				Width:      s.Width,
				Height:     s.Height,
				Duration:   streamDuration(s),
				FPS:        firstFPS(s.AvgFrameRate, s.RFrameRate),
				PixFmt:     s.PixFmt,
				BitDepth:   bitDepth(s.PixFmt, s.BitsPerRawSample),
				FieldOrder: strings.ToLower(s.FieldOrder),
				SAR:        s.SampleAspectRatio,
				HDR:        detectHDR(s),
			}
		case "audio":
			info.Audio = append(info.Audio, Audio{
				Index:    s.Index,
				Codec:    s.CodecName,
				Profile:  s.Profile,
				Channels: s.Channels,
				Language: NormalizeLang(s.Tags["language"]),
				Title:    s.Tags["title"],
				Default:  s.Disposition["default"] == 1,
				Forced:   s.Disposition["forced"] == 1,
				Bitrate:  parseInt(s.BitRate),
			})
		case "subtitle":
			info.Subs = append(info.Subs, Sub{
				Index:    s.Index,
				Codec:    s.CodecName,
				Language: NormalizeLang(s.Tags["language"]),
				Title:    s.Tags["title"],
				Default:  s.Disposition["default"] == 1,
				Forced:   s.Disposition["forced"] == 1,
			})
		}
	}
	if info.Video.Codec == "" {
		return nil, fmt.Errorf("%s: no video stream", path)
	}
	info.estimateVideoBitrate(parseInt(raw.Format.BitRate))
	return info, nil
}

func streamDuration(s ffStream) float64 {
	if seconds := parseFloat(s.Duration); seconds > 0 {
		return seconds
	}
	if seconds := parseFloat(tagValue(s.Tags, "duration")); seconds > 0 {
		return seconds
	}
	tag := tagValue(s.Tags, "duration")
	parts := strings.Split(tag, ":")
	if len(parts) == 3 {
		h := parseFloat(parts[0])
		m := parseFloat(parts[1])
		sec := parseFloat(parts[2])
		if h >= 0 && m >= 0 && sec >= 0 {
			return h*3600 + m*60 + sec
		}
	}
	return 0
}

func tagValue(tags map[string]string, name string) string {
	for key, value := range tags {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

// estimateVideoBitrate derives the video bitrate from the container total when
// the stream itself does not report one, which is the common case for MKV.
func (m *MediaInfo) estimateVideoBitrate(formatBitrate int64) {
	if m.VideoBitrate > 0 {
		return
	}
	total := formatBitrate
	if total == 0 && m.Duration > 0 && m.Size > 0 {
		total = int64(float64(m.Size) * 8 / m.Duration)
	}
	if total == 0 {
		return
	}
	var audio int64
	for _, a := range m.Audio {
		audio += a.BitRate()
	}
	if vb := total - audio; vb > 0 {
		m.VideoBitrate = vb
	}
}

// SampleVideoBitrate measures the video bitrate from real packets over a window
// in the middle of the file. Used by `--accurate` and whenever the container
// offers no usable total.
func (p Prober) SampleVideoBitrate(ctx context.Context, path string, duration float64) (int64, error) {
	window := 60.0
	if duration <= 0 {
		return 0, fmt.Errorf("%s: unknown duration", path)
	}
	if duration < window*3 {
		window = duration / 3
	}
	if window < 1 {
		return 0, fmt.Errorf("%s: too short to sample", path)
	}
	start := duration/2 - window/2
	if start < 0 {
		start = 0
	}

	cmd := exec.CommandContext(ctx, p.bin(),
		"-v", "error",
		"-select_streams", "v:0",
		"-read_intervals", fmt.Sprintf("%.3f%%+%.3f", start, window),
		"-show_entries", "packet=size",
		"-of", "csv=p=0",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return 0, fmt.Errorf("ffprobe %s: %s", path, strings.TrimSpace(string(ee.Stderr)))
		}
		return 0, err
	}

	var bytes int64
	var counted int
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		n, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			continue
		}
		bytes += n
		counted++
	}
	if counted == 0 || bytes == 0 {
		return 0, fmt.Errorf("%s: no packets in sample window", path)
	}
	return int64(float64(bytes) * 8 / window), nil
}

func (p Prober) bin() string {
	if p.FFProbe != "" {
		return p.FFProbe
	}
	return "ffprobe"
}

func detectHDR(s ffStream) HDR {
	h := HDR{}
	switch strings.ToLower(s.ColorTransfer) {
	case "smpte2084":
		h.HDR10 = true
	case "arib-std-b67":
		h.HLG = true
	}
	for _, sd := range s.SideDataList {
		if strings.Contains(strings.ToUpper(sd.SideDataType), "DOVI") {
			h.DoVi = true
			h.DoViProfile = sd.DVProfile
		}
	}
	switch s.CodecTagString {
	case "dvh1", "dvhe", "dav1":
		h.DoVi = true
	}
	return h
}

var pixFmtDepth = regexp.MustCompile(`p(\d{2,3})(?:le|be)?$`)

func bitDepth(pixFmt, raw string) int {
	if n := parseInt(raw); n > 0 {
		return int(n)
	}
	if m := pixFmtDepth.FindStringSubmatch(pixFmt); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return n
		}
	}
	return 8
}

func firstFPS(candidates ...string) float64 {
	for _, c := range candidates {
		if f := parseFPS(c); f > 0 {
			return f
		}
	}
	return 0
}

func parseFPS(s string) float64 {
	if s == "" {
		return 0
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		num, err1 := strconv.ParseFloat(s[:i], 64)
		den, err2 := strconv.ParseFloat(s[i+1:], 64)
		if err1 == nil && err2 == nil && den != 0 {
			return num / den
		}
		return 0
	}
	return parseFloat(s)
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func parseInt(s string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return v
}
