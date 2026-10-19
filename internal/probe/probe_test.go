package probe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func TestParseH264MKVWithSubs(t *testing.T) {
	info, err := parseJSON("x.mkv", loadFixture(t, "h264_aac_srt.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsMatroska() {
		t.Errorf("container = %q, want matroska", info.Container)
	}
	if info.Video.Codec != "h264" || info.Video.Width != 1280 || info.Video.Height != 720 {
		t.Errorf("video = %+v", info.Video)
	}
	if got := info.Video.FPS; got < 23.97 || got > 23.98 {
		t.Errorf("fps = %v, want ~23.976", got)
	}
	if info.Video.BitDepth != 8 {
		t.Errorf("bit depth = %d, want 8", info.Video.BitDepth)
	}
	if info.Video.Interlaced() {
		t.Error("progressive source reported as interlaced")
	}
	if len(info.Audio) != 1 || info.Audio[0].Codec != "aac" || info.Audio[0].Language != "eng" {
		t.Errorf("audio = %+v", info.Audio)
	}
	if !info.Audio[0].Default {
		t.Error("default disposition not carried through")
	}
	if len(info.Subs) != 1 || info.Subs[0].Language != "eng" || info.Subs[0].Image() {
		t.Errorf("subs = %+v", info.Subs)
	}
}

func TestParseHEVC10Bit(t *testing.T) {
	info, err := parseJSON("x.mkv", loadFixture(t, "hevc10_eac3.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Video.Codec != "hevc" {
		t.Fatalf("codec = %q", info.Video.Codec)
	}
	// bits_per_raw_sample is absent for this file, so depth must come from the
	// pixel format: yuv420p10le -> 10.
	if info.Video.BitDepth != 10 {
		t.Errorf("bit depth = %d, want 10", info.Video.BitDepth)
	}
	if info.Video.HDR.Any() {
		t.Error("10-bit SDR detected as HDR")
	}
	if len(info.Audio) != 1 || info.Audio[0].Codec != "eac3" || info.Audio[0].Language != "fre" {
		t.Errorf("audio = %+v", info.Audio)
	}
}

func TestProbeCapturesVideoEncoderTag(t *testing.T) {
	data := []byte(`{
		"format": {"format_name":"matroska,webm", "duration":"600", "size":"100000000", "bit_rate":"1333333"},
		"streams": [{
			"index":0, "codec_type":"video", "codec_name":"hevc",
			"width":1280, "height":720, "avg_frame_rate":"24/1",
			"pix_fmt":"yuv420p", "field_order":"progressive",
			"tags":{"ENCODER":"Lavc59.37.100 hevc_vaapi"}
		}]
	}`)
	info, err := parseJSON("movie.mkv", data)
	if err != nil {
		t.Fatal(err)
	}
	if info.Video.EncoderTag != "Lavc59.37.100 hevc_vaapi" {
		t.Fatalf("encoder tag = %q", info.Video.EncoderTag)
	}
	if !info.Video.HardwareEncoded() {
		t.Fatal("VAAPI HEVC encoder tag was not detected")
	}
}

func TestHardwareEncodedRequiresMatchingHEVCTag(t *testing.T) {
	cases := []struct {
		codec, tag string
		want       bool
	}{
		{"hevc", "Lavc59.37.100 hevc_vaapi", true},
		{"hevc", "Lavc hevc_qsv", true},
		{"hevc", "x265", false},
		{"h264", "Lavc h264_vaapi", false},
		{"av1", "av1_qsv", false},
	}
	for _, c := range cases {
		v := Video{Codec: c.codec, EncoderTag: c.tag}
		if got := v.HardwareEncoded(); got != c.want {
			t.Errorf("HardwareEncoded(%q, %q) = %v, want %v", c.codec, c.tag, got, c.want)
		}
	}
}

func TestParseInterlacedMPEG2AVI(t *testing.T) {
	info, err := parseJSON("x.avi", loadFixture(t, "mpeg2_480i.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Video.Codec != "mpeg2video" {
		t.Fatalf("codec = %q", info.Video.Codec)
	}
	if !info.Video.Interlaced() {
		t.Error("field order bb should be reported as interlaced")
	}
	if info.IsMatroska() {
		t.Error("avi should not be matroska")
	}
	if len(info.Audio) != 1 || info.Audio[0].Language != "" {
		t.Errorf("untagged audio should normalize to empty language, got %+v", info.Audio)
	}
}

func TestNormalizeLang(t *testing.T) {
	cases := map[string]string{
		"eng": "eng", "en": "eng", "EN": "eng", "": "", "und": "",
		"fra": "fre", "fre": "fre", "fr": "fre", "deu": "ger", "ger": "ger",
		"zho": "chi", "cmn": "chi", "jpn": "jpn", "ja": "jpn",
	}
	for in, want := range cases {
		if got := NormalizeLang(in); got != want {
			t.Errorf("NormalizeLang(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOriginalLanguageFromFilename(t *testing.T) {
	cases := map[string]string{
		"Movie.2019.1080p.BluRay.x264-GRP.mkv":      "",
		"Movie.2019.FRENCH.1080p.WEB-DL.mkv":        "fre",
		"Show.S01E01.TRUEFRENCH.720p.mkv":           "fre",
		"Anime.S01E01.JAPANESE.1080p.mkv":           "jpn",
		"Movie.2019.GERMAN.DL.1080p.mkv":            "ger",
		"Movie.2019.MULTI.FRENCH.ENGLISH.1080p.mkv": "",
		"/media/movies/Deutschland.2019.German.mkv": "ger",
	}
	for in, want := range cases {
		if got := OriginalLanguageFromFilename(in); got != want {
			t.Errorf("OriginalLanguageFromFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAudioLossless(t *testing.T) {
	cases := []struct {
		codec, profile string
		want           bool
	}{
		{"truehd", "Dolby TrueHD + Dolby Atmos", true},
		{"truehd", "", true},
		{"flac", "", true},
		{"alac", "", true},
		{"pcm_s24le", "", true},
		{"dts", "DTS-HD MA", true},
		{"dts", "DTS-HD Master Audio", true},
		{"dts", "DTS-HD HRA", false},
		{"dts", "DTS", false},
		{"ac3", "", false},
		{"eac3", "", false},
		{"aac", "", false},
		{"opus", "", false},
	}
	for _, c := range cases {
		a := Audio{Codec: c.codec, Profile: c.profile}
		if got := a.Lossless(); got != c.want {
			t.Errorf("Audio{%s,%q}.Lossless() = %v, want %v", c.codec, c.profile, got, c.want)
		}
	}
}

func TestBPPAndEvictionOfUnknownBitrate(t *testing.T) {
	info := &MediaInfo{
		Size:     100 * 1024 * 1024,
		Duration: 100,
		Video:    Video{Width: 1920, Height: 1080, FPS: 24},
		Audio:    []Audio{{Codec: "ac3", Channels: 6, Bitrate: 640_000}},
	}
	// 100 MiB over 100s = ~8.39 Mbps total; minus 640 kbps audio.
	info.estimateVideoBitrate(0)
	if info.VideoBitrate <= 0 {
		t.Fatal("bitrate not derived from container size")
	}
	want := int64(float64(100*1024*1024)*8/100) - 640_000
	if diff := info.VideoBitrate - want; diff > 1000 || diff < -1000 {
		t.Errorf("video bitrate = %d, want ~%d", info.VideoBitrate, want)
	}
	if bpp := info.BPP(); bpp < 0.15 || bpp > 0.16 {
		t.Errorf("bpp = %v, want ~0.156", bpp)
	}
}

// TestSampleVideoBitrate depends on a real ffmpeg/ffprobe. It is the one test
// that proves the packet-sampling technique actually recovers a known bitrate.
func TestSampleVideoBitrate(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.mkv")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-t", "30", "-i", "testsrc2=size=640x360:rate=25",
		"-c:v", "libx264", "-preset", "ultrafast", "-b:v", "1500k",
		"-minrate", "1500k", "-maxrate", "1500k", "-bufsize", "3000k",
		"-pix_fmt", "yuv420p", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot generate sample: %v: %s", err, out)
	}

	p := New()
	got, err := p.SampleVideoBitrate(context.Background(), path, 30)
	if err != nil {
		t.Fatal(err)
	}
	// CBR-ish target is 1.5 Mbps; allow generous slack for a synthetic source.
	if got < 1_000_000 || got > 2_200_000 {
		t.Errorf("sampled bitrate = %d, want roughly 1.5 Mbps", got)
	}
}
