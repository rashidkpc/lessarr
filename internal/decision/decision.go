// Package decision decides, per file, whether lessarr should leave it alone,
// remux it, or re-encode it — and records why.
//
// The engine is pure: it takes probe facts plus a library policy and returns a
// decision. It never touches the filesystem, which is what makes the whole rule
// set testable against hand-written fixtures for codecs we cannot synthesize.
package decision

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/probe"
)

type Action string

const (
	Skip      Action = "skip"
	Remux     Action = "remux"
	Transcode Action = "transcode"
)

// Code is a stable, machine-readable reason. Tests assert on these so that
// human-facing wording can change without churn.
type Code string

const (
	CodeCompliant           Code = "compliant"
	CodeProvenance          Code = "provenance"
	CodeMonitorMode         Code = "monitor_mode"
	CodeBelowMinDuration    Code = "below_min_duration"
	CodeBelowMinSize        Code = "below_min_size"
	CodeBelowMinLongEdge    Code = "below_min_long_edge"
	CodeHDRPolicy           Code = "hdr_policy"
	CodeTenBitHEVC          Code = "ten_bit_hevc"
	CodeStarvedSource       Code = "starved_source"
	CodeCodecGap            Code = "codec_gap"
	CodeInterlaced          Code = "interlaced"
	CodeBPPWasteful         Code = "bpp_wasteful"
	CodeTranscodeDisallowed Code = "transcode_disallowed"
	CodeLosslessConvert     Code = "lossless_convert"
	CodeLosslessDrop        Code = "lossless_drop"
	CodeAudioPrune          Code = "audio_prune"
	CodeSubPrune            Code = "sub_prune"
	CodeContainerRemux      Code = "container_remux"
	CodeRemuxBelowBar       Code = "remux_below_bar"
)

type Size struct {
	W int `json:"w"`
	H int `json:"h"`
}

// AudioTarget describes an audio re-encode for one source track.
type AudioTarget struct {
	Source   int    `json:"source"`
	Codec    string `json:"codec"`
	Channels int    `json:"channels"`
	Bitrate  int64  `json:"bitrate"`
}

// Decision is the complete plan for one file.
type Decision struct {
	Action  Action   `json:"action"`
	Codes   []Code   `json:"codes"`
	Reasons []string `json:"reasons"`

	Video        int                 `json:"video"`
	AudioKeep    []int               `json:"audio_keep"`
	AudioTargets map[int]AudioTarget `json:"audio_targets,omitempty"`
	SubsKeep     []int               `json:"subtitles_keep"`

	Deinterlace bool  `json:"deinterlace,omitempty"`
	ScaleTo     *Size `json:"scale_to,omitempty"`

	EstimatedSavings     float64 `json:"estimated_savings"` // 0..1
	EstimatedOutputBytes int64   `json:"estimated_output_bytes"`
}

// Options are facts that do not come from the media file itself.
type Options struct {
	Tagged       bool   // output of a previous lessarr run
	OriginalLang string // from the release name, may be ""
}

func (d Decision) Has(c Code) bool {
	for _, x := range d.Codes {
		if x == c {
			return true
		}
	}
	return false
}

// String renders the decision the way the CLI and the dashboard show it.
func (d Decision) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s", d.Action)
	if len(d.Codes) > 0 {
		parts := make([]string, len(d.Codes))
		for i, c := range d.Codes {
			parts[i] = string(c)
		}
		fmt.Fprintf(&b, " [%s]", strings.Join(parts, ", "))
	}
	if d.EstimatedSavings > 0 {
		fmt.Fprintf(&b, " ~%.0f%% smaller", d.EstimatedSavings*100)
	}
	for _, r := range d.Reasons {
		fmt.Fprintf(&b, "\n  - %s", r)
	}
	return b.String()
}

// Decide applies the policy.
func Decide(info *probe.MediaInfo, lib config.Library, pol Policy, opts Options) Decision {
	d := Decision{Video: info.Video.Index}

	// Hard stops, in order of precedence.
	if opts.Tagged {
		return skip(CodeProvenance, "previously encoded by lessarr; never re-encoded")
	}
	if lib.Mode == "monitor" {
		return skip(CodeMonitorMode, "library is in monitor mode")
	}
	// Duration is the real sample filter. Fail open when the container does not
	// report a runtime: missing metadata must never cause a file to be ignored.
	if lib.MinDuration > 0 && info.Duration > 0 && info.Duration < lib.MinDuration.Std().Seconds() {
		return skipf(CodeBelowMinDuration, "runtime %s below min_duration %s",
			humanDuration(info.Duration), lib.MinDuration.Std())
	}
	if info.Size < lib.MinSizeMB*config.MB {
		return skipf(CodeBelowMinSize, "size %s below min_size_mb %d", humanBytes(info.Size), lib.MinSizeMB)
	}
	if info.MaxLongEdge() < lib.MinLongEdge {
		return skipf(CodeBelowMinLongEdge, "resolution %dx%d below min_long_edge %d",
			info.Video.Width, info.Video.Height, lib.MinLongEdge)
	}
	if lib.HDR == "skip" && info.Video.HDR.Any() {
		return skip(CodeHDRPolicy, hdrLabel(info.Video.HDR)+" excluded by library policy")
	}
	if !lib.Allow10Bit && info.Video.Codec == "hevc" && info.Video.BitDepth >= 10 {
		return skip(CodeTenBitHEVC, "10-bit HEVC is never re-encoded (this GPU outputs 8-bit)")
	}

	audio := planAudio(info, pol.Audio, opts.OriginalLang)
	subs := planSubs(info, pol.Subtitles)

	// Video verdict.
	transcode := false
	var videoCodes []Code
	var videoReasons []string

	switch {
	case !isModern(info.Video.Codec):
		if bpp := info.BPP(); bpp > 0 && bpp < floorForSource(info) {
			return skipf(CodeStarvedSource,
				"%s at bpp %.3f is already heavily compressed; re-encoding would compound artifacts",
				codecLabel(info.Video.Codec), bpp)
		}
		transcode = true
		videoCodes = append(videoCodes, CodeCodecGap)
		videoReasons = append(videoReasons, fmt.Sprintf("%s video", codecLabel(info.Video.Codec)))
		if info.Video.Interlaced() {
			videoCodes = append(videoCodes, CodeInterlaced)
			videoReasons = append(videoReasons, "interlaced, will deinterlace")
		}
	case info.Video.Interlaced():
		transcode = true
		videoCodes = append(videoCodes, CodeInterlaced)
		videoReasons = append(videoReasons, "interlaced, will deinterlace")
	default:
		if bpp := info.BPP(); bpp > 0 && bpp >= wastefulFor(info) {
			transcode = true
			videoCodes = append(videoCodes, CodeBPPWasteful)
			videoReasons = append(videoReasons,
				fmt.Sprintf("%s at bpp %.3f is above the %.2f wasteful threshold",
					codecLabel(info.Video.Codec), bpp, wastefulFor(info)))
		}
	}

	if transcode && lib.Mode == "remux" {
		return skip(CodeTranscodeDisallowed, "video needs re-encoding but library mode is remux-only")
	}

	containerNeedsRemux, containerReason := needsContainerRemux(info)

	if transcode {
		d.Action = Transcode
		d.Codes = append(videoCodes, audio.Codes...)
		d.Codes = append(d.Codes, subs.Codes...)
		d.Reasons = append(videoReasons, audio.Reasons...)
		d.Reasons = append(d.Reasons, subs.Reasons...)
		d.AudioKeep = audio.Keep
		d.AudioTargets = audio.Targets
		d.SubsKeep = subs.Keep
		d.Deinterlace = info.Video.Interlaced()
		d.ScaleTo = targetSize(info, lib.MaxLongEdge)

		saved := estimateVideoSavings(info) + audio.SavedBytes
		d.EstimatedSavings = clamp01(float64(saved) / float64(info.Size))
		d.EstimatedOutputBytes = max64(0, info.Size-saved)
		d.Reasons = append(d.Reasons,
			fmt.Sprintf("estimated savings %.0f%%", d.EstimatedSavings*100))
		return d
	}

	// Video is fine. A remux is only worth it if it reclaims real bytes or
	// fixes a container that cannot represent the tracks properly.
	if containerNeedsRemux || audio.SavedBytes >= lib.MinRemuxBytes*config.MB {
		d.Action = Remux
		d.Codes = append(d.Codes, audio.Codes...)
		if containerNeedsRemux {
			d.Codes = append(d.Codes, CodeContainerRemux)
			d.Reasons = append(d.Reasons, containerReason)
		}
		if len(subs.Dropped) > 0 {
			d.Codes = append(d.Codes, CodeSubPrune)
		}
		d.Reasons = append(d.Reasons, audio.Reasons...)
		d.Reasons = append(d.Reasons, subs.Reasons...)
		d.AudioKeep = audio.Keep
		d.AudioTargets = audio.Targets
		d.SubsKeep = subs.Keep

		saved := audio.SavedBytes
		d.EstimatedSavings = clamp01(float64(saved) / float64(info.Size))
		d.EstimatedOutputBytes = max64(0, info.Size-saved)
		return d
	}

	// Nothing worth doing. Say why the tempting changes were declined.
	d.Action = Skip
	d.Codes = []Code{CodeCompliant}
	d.Reasons = append(d.Reasons, fmt.Sprintf(
		"compliant: %s at bpp %.3f, tracks within policy",
		codecLabel(info.Video.Codec), info.BPP()))
	if audio.SavedBytes > 0 || len(subs.Dropped) > 0 {
		d.Codes = append(d.Codes, CodeRemuxBelowBar)
		d.Reasons = append(d.Reasons, fmt.Sprintf(
			"track pruning would reclaim %s, below the %d MB remux bar",
			humanBytes(audio.SavedBytes), lib.MinRemuxBytes))
	}
	return d
}

// Policy bundles the non-library settings the engine needs.
type Policy struct {
	Audio     config.Audio
	Subtitles config.Subtitles
}

func skip(c Code, reason string) Decision {
	return Decision{Action: Skip, Codes: []Code{c}, Reasons: []string{reason}}
}

func skipf(c Code, format string, args ...any) Decision {
	return skip(c, fmt.Sprintf(format, args...))
}

func isModern(codec string) bool {
	switch codec {
	case "hevc", "av1":
		return true
	}
	return false
}

func codecLabel(codec string) string {
	switch codec {
	case "mpeg2video":
		return "mpeg2"
	case "mpeg4":
		return "mpeg4 (xvid)"
	}
	return codec
}

func hdrLabel(h probe.HDR) string {
	switch {
	case h.DoVi:
		if h.DoViProfile > 0 {
			return fmt.Sprintf("dolby vision profile %d", h.DoViProfile)
		}
		return "dolby vision"
	case h.HLG:
		return "hlg"
	case h.HDR10:
		return "hdr10"
	}
	return "hdr"
}

func channelsLabel(n int) string {
	switch n {
	case 1:
		return "mono"
	case 2:
		return "2.0"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	}
	return strconv.Itoa(n) + "ch"
}

func targetSize(info *probe.MediaInfo, maxLongEdge int) *Size {
	if maxLongEdge <= 0 {
		return nil
	}
	w, h := info.Video.Width, info.Video.Height
	long := max(w, h)
	if long <= maxLongEdge {
		return nil
	}
	f := float64(maxLongEdge) / float64(long)
	return &Size{W: even(int(math.Round(float64(w) * f))), H: even(int(math.Round(float64(h) * f)))}
}

func even(n int) int {
	if n%2 != 0 {
		n--
	}
	return max(n, 2)
}

func estimateVideoSavings(info *probe.MediaInfo) int64 {
	if info.VideoBitrate == 0 || info.Duration == 0 {
		return 0
	}
	f := codecGap(info.Video.Codec)
	return int64(float64(info.VideoBitrate) * f * info.Duration / 8)
}

func needsContainerRemux(info *probe.MediaInfo) (bool, string) {
	if info.IsMatroska() {
		return false, ""
	}
	c := info.Container
	if strings.Contains(c, "mp4") || strings.Contains(c, "mov") {
		if len(info.Audio) > 1 {
			return true, "container mp4 with multiple audio tracks"
		}
		for _, s := range info.Subs {
			if s.Image() {
				return true, "container mp4 with image subtitles"
			}
		}
		return false, ""
	}
	return true, "container " + c
}

type audioPlan struct {
	Keep       []int
	Targets    map[int]AudioTarget
	Dropped    []int
	SavedBytes int64
	Codes      []Code
	Reasons    []string
}

func planAudio(info *probe.MediaInfo, pol config.Audio, originalLang string) audioPlan {
	p := audioPlan{Targets: map[int]AudioTarget{}}
	if len(info.Audio) == 0 {
		return p
	}

	keep := map[int]bool{}
	for _, a := range info.Audio {
		switch {
		case pol.KeepDefault && a.Default:
			keep[a.Index] = true
		case len(pol.KeepLanguages) > 0 && langIn(a.Language, pol.KeepLanguages):
			keep[a.Index] = true
		case pol.KeepOriginal && originalLang != "" && a.Language == originalLang:
			keep[a.Index] = true
		}
	}
	// Never produce a file with no audio at all.
	if len(keep) == 0 {
		keep[info.Audio[0].Index] = true
	}

	// Which languages already have a kept lossy track? A lossless track in one
	// of those languages is redundant, so drop it rather than re-encode it.
	lossyLangs := map[string]bool{}
	for _, a := range info.Audio {
		if keep[a.Index] && !a.Lossless() {
			lossyLangs[a.Language] = true
		}
	}

	for _, a := range info.Audio {
		if !keep[a.Index] {
			p.Dropped = append(p.Dropped, a.Index)
			p.SavedBytes += trackBytes(a.BitRate(), info.Duration)
			continue
		}
		if !a.Lossless() || !pol.LosslessToEAC3 {
			continue
		}
		if lossyLangs[a.Language] {
			delete(keep, a.Index)
			p.Dropped = append(p.Dropped, a.Index)
			p.SavedBytes += trackBytes(a.BitRate(), info.Duration)
			p.Codes = append(p.Codes, CodeLosslessDrop)
			p.Reasons = append(p.Reasons, fmt.Sprintf(
				"%s %s dropped (%s track retained)",
				codecLabel(a.Codec), channelsLabel(a.Channels), langLabel(a.Language)))
			continue
		}
		ch := a.Channels
		if ch < 1 {
			ch = 2
		}
		if ch > 6 {
			ch = 6 // ffmpeg's E-AC-3 encoder caps at 5.1
		}
		br := eac3Bitrate(pol, ch)
		p.Targets[a.Index] = AudioTarget{Source: a.Index, Codec: "eac3", Channels: ch, Bitrate: br}
		p.SavedBytes += trackBytes(a.BitRate()-br, info.Duration)
		p.Codes = append(p.Codes, CodeLosslessConvert)
		p.Reasons = append(p.Reasons, fmt.Sprintf(
			"%s %s -> eac3 %s %s",
			codecLabel(a.Codec), channelsLabel(a.Channels), channelsLabel(ch), bitrateLabel(br)))
	}

	// Dropped-but-kept-language tracks are just pruning, not lossless handling.
	if len(p.Dropped) > 0 {
		p.Codes = append(p.Codes, CodeAudioPrune)
	}
	if len(p.Dropped) > 0 && !hasCode(p.Codes, CodeLosslessDrop) {
		p.Reasons = append(p.Reasons, fmt.Sprintf("pruned %d audio track(s)", len(p.Dropped)))
	}

	for idx := range keep {
		p.Keep = append(p.Keep, idx)
	}
	sort.Ints(p.Keep)
	if p.SavedBytes < 0 {
		p.SavedBytes = 0
	}
	return p
}

type subPlan struct {
	Keep    []int
	Dropped []int
	Codes   []Code
	Reasons []string
}

func planSubs(info *probe.MediaInfo, pol config.Subtitles) subPlan {
	var p subPlan
	for _, s := range info.Subs {
		switch {
		case pol.KeepForced && s.Forced:
			p.Keep = append(p.Keep, s.Index)
		case len(pol.KeepLanguages) > 0 && langIn(s.Language, pol.KeepLanguages):
			p.Keep = append(p.Keep, s.Index)
		default:
			p.Dropped = append(p.Dropped, s.Index)
		}
	}
	if len(p.Dropped) > 0 {
		p.Codes = append(p.Codes, CodeSubPrune)
		p.Reasons = append(p.Reasons, fmt.Sprintf("pruned %d subtitle track(s)", len(p.Dropped)))
	}
	return p
}

func langIn(lang string, allowed []string) bool {
	if lang == "" {
		return false
	}
	for _, a := range allowed {
		if probe.NormalizeLang(a) == lang {
			return true
		}
	}
	return false
}

func langLabel(lang string) string {
	if lang == "" {
		return "und"
	}
	return lang
}

func eac3Bitrate(pol config.Audio, channels int) int64 {
	s := pol.EAC3Bitrate20
	if channels >= 6 {
		s = pol.EAC3Bitrate51
	}
	return parseBitrate(s, int64(channels)*128_000)
}

func parseBitrate(s string, fallback int64) int64 {
	s = strings.TrimSpace(strings.ToLower(s))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "k"):
		mult, s = 1000, strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		mult, s = 1_000_000, strings.TrimSuffix(s, "m")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return fallback
	}
	return n * mult
}

func bitrateLabel(bps int64) string {
	if bps >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(bps)/1_000_000)
	}
	return fmt.Sprintf("%dk", bps/1000)
}

func trackBytes(bps int64, duration float64) int64 {
	if bps <= 0 || duration <= 0 {
		return 0
	}
	return int64(float64(bps) * duration / 8)
}

func hasCode(codes []Code, c Code) bool {
	for _, x := range codes {
		if x == c {
			return true
		}
	}
	return false
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func humanDuration(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	if d <= 0 {
		return "unknown"
	}
	if h := int(d.Hours()); h > 0 {
		return fmt.Sprintf("%dh%dm", h, int(d.Minutes())%60)
	}
	if m := int(d.Minutes()); m > 0 {
		return fmt.Sprintf("%dm%ds", m, int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
