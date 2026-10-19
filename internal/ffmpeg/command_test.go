package ffmpeg

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/decision"
	"github.com/rashidkpc/lessarr/internal/probe"
)

func sampleInfo() *probe.MediaInfo {
	return &probe.MediaInfo{
		Path:         "/media/movies/Example (2019)/Example.mkv",
		Size:         8 * 1024 * 1024 * 1024,
		Duration:     7200,
		Container:    "matroska,webm",
		VideoBitrate: 8_000_000,
		Video:        probe.Video{Index: 0, Codec: "h264", Width: 1920, Height: 1080, FPS: 23.976, BitDepth: 8},
	}
}

func sampleDecision() decision.Decision {
	return decision.Decision{
		Action:       decision.Transcode,
		Video:        0,
		AudioKeep:    []int{1, 2},
		AudioTargets: map[int]decision.AudioTarget{2: {Source: 2, Codec: "eac3", Channels: 6, Bitrate: 640000}},
		SubsKeep:     []int{4},
		Deinterlace:  true,
		ScaleTo:      &decision.Size{W: 1920, H: 1080},
	}
}

// Golden argv tests are how the hardware paths are validated on a machine that
// has neither a GPU nor the drivers. If a flag regresses, it fails here rather
// than on the library.
func TestBuildTranscodeVAAPI(t *testing.T) {
	lib := config.DefaultLibrary()
	got, err := Build(VAAPI("/dev/dri/renderD128"), "in.mkv", "out.mkv", sampleInfo(), sampleDecision(), lib)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-hide_banner", "-nostdin", "-y",
		"-hwaccel", "vaapi", "-hwaccel_device", "/dev/dri/renderD128", "-hwaccel_output_format", "vaapi",
		"-i", "in.mkv",
		"-map", "0:0",
		"-map", "0:1", "-c:a:0", "copy",
		"-map", "0:2", "-c:a:1", "eac3", "-ac:a:1", "6", "-b:a:1", "640000",
		"-map", "0:4", "-c:s:0", "copy",
		"-vf", "deinterlace_vaapi=rate=frame,scale_vaapi=w=1920:h=1080:format=nv12",
		"-c:v", "hevc_vaapi", "-rc_mode", "CQP", "-qp", "24",
		"-map_metadata", "0", "-map_chapters", "0",
		"-metadata", "LESSARR_ENCODED=1",
		"-max_muxing_queue_size", "4096",
		"-progress", "pipe:1", "-nostats",
		"out.mkv",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv mismatch\n got: %s\nwant: %s", strings.Join(got, " "), strings.Join(want, " "))
	}
}

func TestBuildTranscodeQSV(t *testing.T) {
	lib := config.DefaultLibrary()
	got, err := Build(QSV("/dev/dri/renderD128"), "in.mkv", "out.mkv", sampleInfo(), sampleDecision(), lib)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	for _, want := range []string{
		"-hwaccel qsv -qsv_device /dev/dri/renderD128 -hwaccel_output_format qsv",
		"-vf deinterlace_qsv,vpp_qsv=w=1920:h=1080:format=nv12",
		"-c:v hevc_qsv -preset slow -global_quality 24",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q\n got: %s", want, joined)
		}
	}
}

func TestBuildTranscodeSoftware(t *testing.T) {
	lib := config.DefaultLibrary()
	got, err := Build(Software(), "in.mkv", "out.mkv", sampleInfo(), sampleDecision(), lib)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "-hwaccel") {
		t.Errorf("software backend must not request hardware accel: %s", joined)
	}
	if !strings.Contains(joined, "-vf yadif,scale=1920:1080,format=yuv420p") {
		t.Errorf("unexpected filter chain: %s", joined)
	}
	if !strings.Contains(joined, "-c:v libx265 -crf 24") {
		t.Errorf("unexpected video args: %s", joined)
	}
}

func TestConfiguredPresetReachesQSVAndSoftware(t *testing.T) {
	d := sampleDecision()
	lib := config.DefaultLibrary()
	lib.Preset = "medium"

	for _, backend := range []Backend{QSV("/dev/dri/renderD128"), Software()} {
		got, err := Build(backend, "in.mkv", "out.mkv", sampleInfo(), d, lib)
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(got, " ")
		if !strings.Contains(joined, "-preset medium") {
			t.Errorf("%s ignored configured preset: %s", backend.Name, joined)
		}
	}

	// VAAPI has no preset option; its rate-control mode and QP are the quality
	// controls available in this backend.
	vaapi, err := Build(VAAPI("/dev/dri/renderD128"), "in.mkv", "out.mkv", sampleInfo(), d, lib)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(vaapi, " "), "-preset") {
		t.Fatal("VAAPI command should not invent a preset flag")
	}
}

// TestNoScaleFilterIsStillFormatConversion guards the resolution bug: with no
// scaling requested, the VAAPI path must still normalise to nv12 (which is what
// turns 10-bit p010 sources into something this GPU can encode), and must never
// reintroduce a height-based scale.
func TestNoScaleFilterIsStillFormatConversion(t *testing.T) {
	d := sampleDecision()
	d.ScaleTo = nil
	d.Deinterlace = false

	got, err := Build(VAAPI("/dev/dri/renderD128"), "in.mkv", "out.mkv", sampleInfo(), d, config.DefaultLibrary())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "-vf scale_vaapi=format=nv12") {
		t.Errorf("expected nv12 normalisation, got: %s", joined)
	}
	for _, bad := range []string{"h=1080", "w=-2", "h=-2"} {
		if strings.Contains(joined, bad) {
			t.Errorf("height-based scaling sneaked back in: %s", joined)
		}
	}
}

func TestVAAPIUploadsSoftwareDecodedFrames(t *testing.T) {
	d := sampleDecision()
	got, err := BuildWithHardwareDecode(VAAPI("/dev/dri/renderD128"), "in.avi", "out.mkv", sampleInfo(), d, config.DefaultLibrary(), false)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	for _, want := range []string{
		"-init_hw_device vaapi=va:/dev/dri/renderD128 -filter_hw_device va",
		"-vf format=nv12,hwupload,deinterlace_vaapi=rate=frame,scale_vaapi=w=1920:h=1080:format=nv12",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("CPU-decoded VAAPI path missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "-hwaccel vaapi") {
		t.Errorf("CPU-decode path must not request a VAAPI decoder: %s", joined)
	}
}

func TestBuildRemuxCopiesVideo(t *testing.T) {
	d := decision.Decision{
		Action:       decision.Remux,
		Video:        0,
		AudioKeep:    []int{2},
		AudioTargets: map[int]decision.AudioTarget{2: {Source: 2, Codec: "eac3", Channels: 6, Bitrate: 640000}},
		SubsKeep:     []int{3},
	}
	got, err := Build(VAAPI("/dev/dri/renderD128"), "in.mkv", "out.mkv", sampleInfo(), d, config.DefaultLibrary())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "-c:v copy") {
		t.Errorf("remux must copy video: %s", joined)
	}
	if strings.Contains(joined, "hevc_vaapi") || strings.Contains(joined, "-vf") {
		t.Errorf("remux must not encode or filter: %s", joined)
	}
	if !strings.Contains(joined, "-c:a:0 eac3 -ac:a:0 6 -b:a:0 640000") {
		t.Errorf("audio conversion missing: %s", joined)
	}
}

func TestMOVTextSubtitleConvertsForMatroska(t *testing.T) {
	info := &probe.MediaInfo{
		Video: probe.Video{Index: 0, Codec: "hevc"},
		Subs:  []probe.Sub{{Index: 3, Codec: "mov_text", Language: "eng"}},
	}
	d := decision.Decision{Action: decision.Remux, Video: 0, SubsKeep: []int{3}}
	got := BuildRemux("in.mp4", "out.mkv", info, d)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "-map 0:3 -c:s:0 srt") {
		t.Errorf("mov_text must be converted to SRT for MKV: %s", joined)
	}
}

func TestMP4OutputKeepsMOVTextAndWritesCustomMetadata(t *testing.T) {
	info := &probe.MediaInfo{
		Video: probe.Video{Index: 0, Codec: "h264"},
		Subs:  []probe.Sub{{Index: 2, Codec: "mov_text", Language: "eng"}},
	}
	d := decision.Decision{Action: decision.Transcode, Video: 0, SubsKeep: []int{2}}
	args := BuildTranscodeForContainer(Software(), "in.mp4", "out.mp4", info, d, config.DefaultLibrary(), false, "mp4")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-map 0:2 -c:s:0 copy") {
		t.Errorf("mov_text should be copied into MP4: %s", joined)
	}
	if !strings.Contains(joined, "-movflags use_metadata_tags") {
		t.Errorf("MP4 output needs custom metadata support for the loop marker: %s", joined)
	}
}

func TestBuildRefusesSkip(t *testing.T) {
	_, err := Build(Software(), "in.mkv", "out.mkv", sampleInfo(), decision.Decision{Action: decision.Skip}, config.DefaultLibrary())
	if err == nil {
		t.Fatal("expected an error for a skip decision")
	}
}
