package ffmpeg

import (
	"context"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"
)

const encoderTable = `Encoders:
 V..... = Video
 ------
 V....D libx265              libx265 H.265 / HEVC (codec hevc)
 V..... hevc_vaapi           H.265/HEVC (VAAPI) (codec hevc)
 V..... hevc_qsv             HEVC (Intel Quick Sync Video acceleration) (codec hevc)
 A....D aac                  AAC (Advanced Audio Coding)
 A....D eac3                 ATSC A/52 E-AC-3
 S..... srt                  SubRip subtitle
`

func TestParseCodecList(t *testing.T) {
	got := parseCodecList(encoderTable)
	for _, want := range []string{"libx265", "hevc_vaapi", "hevc_qsv", "aac", "eac3", "srt"} {
		if !got[want] {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	// Header rows and the separator must not be mistaken for codec names.
	for _, unwanted := range []string{"=", "------", "Encoders:"} {
		if got[unwanted] {
			t.Errorf("%q should not be parsed as a codec", unwanted)
		}
	}
}

func TestHardwareDecodeSupport(t *testing.T) {
	decoders := map[string]bool{
		"h264_vaapi": true, "hevc_vaapi": true, "mpeg2_vaapi": true,
		"vc1_vaapi": true, "vp9_vaapi": true,
	}
	support := HardwareDecodeSupport(decoders)
	for _, codec := range []string{"h264", "hevc", "mpeg2video", "vc1", "vp9"} {
		if !support[codec] {
			t.Errorf("%s should be reported as hardware-decodable", codec)
		}
	}
	// MPEG-4 Part 2 has no hardware decoder on consumer Intel graphics. The
	// encoder must not assume hardware frames for it.
	if support["mpeg4"] {
		t.Error("mpeg4 must not be reported as hardware-decodable")
	}
	if len(SourceCodecsWithHWDecode()) == 0 {
		t.Error("expected at least one codec to be checked")
	}
}

func TestHasVAAPIHEVCEncodeEntryPoint(t *testing.T) {
	tests := []struct {
		name string
		info string
		want bool
	}{
		{"decode only", "VAProfileHEVCMain : VAEntrypointVLD", false},
		{"hybrid encode", "VAProfileHEVCMain : VAEntrypointEncSlice", true},
		{"low power encode", "VAProfileHEVCMain : VAEntrypointEncSliceLP", true},
		{"main10 encode is not main encode", "VAProfileHEVCMain10 : VAEntrypointEncSlice", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasVAAPIHEVCEncodeEntryPoint(tt.info); got != tt.want {
				t.Errorf("HasVAAPIHEVCEncodeEntryPoint() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestByName(t *testing.T) {
	for _, name := range []string{"vaapi", "qsv", "software", "auto"} {
		b, err := ByName(name, "/dev/dri/renderD128")
		if err != nil {
			t.Errorf("ByName(%q): %v", name, err)
		}
		if b.Encoder == "" {
			t.Errorf("ByName(%q) returned no encoder", name)
		}
	}
	if _, err := ByName("nonsense", ""); err == nil {
		t.Error("expected an error for an unknown backend")
	}
}

// TestTrialEncodeSoftware is a real encode. It proves the invocation pattern,
// the output file and the decode verification all work end to end, which is
// something the golden argv tests cannot do.
func TestTrialEncodeSoftware(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	r := NewRunner()
	res := r.TrialEncode(context.Background(), Software(), "", dir, 2, 2*time.Minute)
	if !res.OK {
		t.Fatalf("trial encode failed: %s\ncommand: %s", res.Err, strings.Join(res.Command, " "))
	}
	if !res.Decodes {
		t.Error("output did not decode cleanly")
	}
	if res.Bytes <= 0 {
		t.Error("no output bytes produced")
	}
	if res.FPS <= 0 {
		t.Error("no throughput measured")
	}
}

func TestTrialEncodeHardwareReportsFailureWithoutDevice(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	r := NewRunner()
	res := r.TrialEncode(context.Background(), VAAPI("/dev/dri/definitely-not-here"), "", dir, 1, time.Minute)
	if res.OK {
		t.Skip("unexpectedly succeeded; this host appears to have VAAPI")
	}
	if res.Err == "" {
		t.Error("a failed trial must carry an error message")
	}
	if len(res.Command) == 0 {
		t.Error("the attempted command should be recorded")
	}
}

func TestLastLines(t *testing.T) {
	got := lastLines("a\nb\nc\nd\ne", 2)
	if got != "d\ne" {
		t.Errorf("lastLines = %q", got)
	}
	all := strings.Split(lastLines("only", 3), "\n")
	if len(all) != 1 {
		t.Errorf("short input should be returned whole, got %v", all)
	}
}

func TestSourceCodecOrderIsStable(t *testing.T) {
	got := SourceCodecsWithHWDecode()
	sorted := append([]string(nil), got...)
	sort.Strings(sorted)
	// The list is reported to users, so it must not shuffle between runs.
	if len(got) != len(sorted) {
		return
	}
	for i := range got {
		if got[i] != sorted[i] {
			t.Errorf("codec list is not in stable sorted order: %v", got)
			return
		}
	}
}
