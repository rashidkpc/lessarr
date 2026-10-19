// Package ffmpeg builds encoder command lines.
//
// Backends are values rather than a hierarchy so that the exact argv for a
// hardware path can be asserted in tests on a machine that has no GPU. That is
// the only way this project stays correct on a box it cannot run on.
package ffmpeg

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/rashidkpc/lessarr/internal/decision"
)

// Backend describes one way of encoding HEVC.
type Backend struct {
	Name               string
	InputArgs          []string // before -i when hardware decode is available
	SoftwareDecodeArgs []string // device setup when decoding on the CPU then uploading
	UploadFilter       string
	Encoder            string                                // e.g. hevc_vaapi
	EncArgs            func(icq int, preset string) []string // after -vf
	Filter             func(deinterlace bool, scale *decision.Size) string
}

// VAAPI is the preferred backend on Intel Linux, including Gen9.5 where the QSV
// runtime path is fragile.
func VAAPI(device string) Backend {
	return Backend{
		Name: "vaapi",
		InputArgs: []string{
			"-hwaccel", "vaapi",
			"-hwaccel_device", device,
			"-hwaccel_output_format", "vaapi",
		},
		SoftwareDecodeArgs: []string{"-init_hw_device", "vaapi=va:" + device, "-filter_hw_device", "va"},
		UploadFilter:       "format=nv12,hwupload",
		Encoder:            "hevc_vaapi",
		EncArgs: func(icq int, _ string) []string {
			return []string{"-c:v", "hevc_vaapi", "-rc_mode", "CQP", "-qp", strconv.Itoa(icq)}
		},
		Filter: func(deinterlace bool, scale *decision.Size) string {
			var parts []string
			if deinterlace {
				parts = append(parts, "deinterlace_vaapi=rate=frame")
			}
			if scale != nil {
				parts = append(parts, fmt.Sprintf("scale_vaapi=w=%d:h=%d:format=nv12", scale.W, scale.H))
			} else {
				// Still needed: converts 10-bit p010 down to the 8-bit nv12
				// this class of GPU can encode, without a CPU round trip.
				parts = append(parts, "scale_vaapi=format=nv12")
			}
			return strings.Join(parts, ",")
		},
	}
}

// QSV uses Intel's Quick Sync path. On Gen9.5 this needs the legacy Media SDK
// runtime, which is why it is the fallback rather than the default.
func QSV(device string) Backend {
	return Backend{
		Name: "qsv",
		InputArgs: []string{
			"-hwaccel", "qsv",
			"-qsv_device", device,
			"-hwaccel_output_format", "qsv",
		},
		SoftwareDecodeArgs: []string{"-init_hw_device", "qsv=hw:" + device, "-filter_hw_device", "hw"},
		UploadFilter:       "format=nv12,hwupload=extra_hw_frames=64",
		Encoder:            "hevc_qsv",
		EncArgs: func(icq int, preset string) []string {
			return []string{"-c:v", "hevc_qsv", "-preset", presetOrDefault(preset), "-global_quality", strconv.Itoa(icq)}
		},
		Filter: func(deinterlace bool, scale *decision.Size) string {
			var parts []string
			if deinterlace {
				parts = append(parts, "deinterlace_qsv")
			}
			if scale != nil {
				parts = append(parts, fmt.Sprintf("vpp_qsv=w=%d:h=%d:format=nv12", scale.W, scale.H))
			} else {
				parts = append(parts, "vpp_qsv=format=nv12")
			}
			return strings.Join(parts, ",")
		},
	}
}

// Software is libx265. It exists so the entire pipeline can be tested without a
// GPU, and so lessarr is not Intel-only.
func Software() Backend {
	return Backend{
		Name:    "software",
		Encoder: "libx265",
		EncArgs: func(icq int, preset string) []string {
			return []string{"-c:v", "libx265", "-crf", strconv.Itoa(icq), "-preset", presetOrDefault(preset), "-pix_fmt", "yuv420p"}
		},
		Filter: func(deinterlace bool, scale *decision.Size) string {
			var parts []string
			if deinterlace {
				parts = append(parts, "yadif")
			}
			if scale != nil {
				parts = append(parts, fmt.Sprintf("scale=%d:%d", scale.W, scale.H))
			}
			parts = append(parts, "format=yuv420p")
			return strings.Join(parts, ",")
		},
	}
}

func presetOrDefault(preset string) string {
	if strings.TrimSpace(preset) == "" {
		return "slow"
	}
	return preset
}

// ByName resolves a configured backend name. "auto" is resolved elsewhere by
// probing the host, not here.
func ByName(name, device string) (Backend, error) {
	switch name {
	case "vaapi":
		return VAAPI(device), nil
	case "qsv":
		return QSV(device), nil
	case "software", "auto":
		return Software(), nil
	}
	return Backend{}, fmt.Errorf("unknown encoder backend %q", name)
}
