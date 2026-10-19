package ffmpeg

import (
	"fmt"
	"strconv"

	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/decision"
	"github.com/rashidkpc/lessarr/internal/probe"
)

// ProvenanceTag marks files this tool produced. The scanner refuses to touch
// anything carrying it, which is what makes re-encode loops impossible even if
// the database is lost.
const ProvenanceTag = "LESSARR_ENCODED"

// Build renders the argv for a decision. The stream mapping comes from the
// decision verbatim; this function never re-decides anything.
func Build(b Backend, src, dst string, info *probe.MediaInfo, d decision.Decision, lib config.Library) ([]string, error) {
	return BuildWithHardwareDecodeAndContainer(b, src, dst, info, d, lib, true, "matroska")
}

// BuildWithHardwareDecode builds a command using hardware decoding when the
// selected device has a decoder for the source codec. Otherwise hardware
// encoders explicitly upload software-decoded frames instead of asking
// scale_vaapi/vpp_qsv to consume incompatible system-memory frames.
func BuildWithHardwareDecode(b Backend, src, dst string, info *probe.MediaInfo, d decision.Decision, lib config.Library, hardwareDecode bool) ([]string, error) {
	return BuildWithHardwareDecodeAndContainer(b, src, dst, info, d, lib, hardwareDecode, "matroska")
}

// BuildWithHardwareDecodeAndContainer keeps the muxer consistent with the
// destination extension. MP4/M4V sources remain ISO-BMFF; Matroska sources stay
// Matroska. Other format changes must be handled by an explicit rename plan.
func BuildWithHardwareDecodeAndContainer(b Backend, src, dst string, info *probe.MediaInfo, d decision.Decision, lib config.Library, hardwareDecode bool, outputFormat string) ([]string, error) {
	switch d.Action {
	case decision.Transcode:
		return BuildTranscodeForContainer(b, src, dst, info, d, lib, hardwareDecode, outputFormat), nil
	case decision.Remux:
		return BuildRemuxForContainer(src, dst, info, d, outputFormat), nil
	case decision.Skip:
		return nil, fmt.Errorf("no command for a skip decision")
	}
	return nil, fmt.Errorf("unknown action %q", d.Action)
}

// BuildTranscode re-encodes the video stream and applies the track plan.
func BuildTranscode(b Backend, src, dst string, info *probe.MediaInfo, d decision.Decision, lib config.Library) []string {
	return BuildTranscodeForContainer(b, src, dst, info, d, lib, true, "matroska")
}

func BuildTranscodeWithHardwareDecode(b Backend, src, dst string, info *probe.MediaInfo, d decision.Decision, lib config.Library, hardwareDecode bool) []string {
	return BuildTranscodeForContainer(b, src, dst, info, d, lib, hardwareDecode, "matroska")
}

func BuildTranscodeForContainer(b Backend, src, dst string, info *probe.MediaInfo, d decision.Decision, lib config.Library, hardwareDecode bool, outputFormat string) []string {
	args := []string{"-hide_banner", "-nostdin", "-y"}
	cpuDecodeUpload := b.Name != "software" && !hardwareDecode
	if cpuDecodeUpload {
		args = append(args, b.SoftwareDecodeArgs...)
	} else {
		args = append(args, b.InputArgs...)
	}
	args = append(args, "-i", src)

	args = append(args, "-map", fmt.Sprintf("0:%d", d.Video))
	args = appendAudioMaps(args, d)
	args = appendSubMaps(args, d, info, outputFormat)

	filter := b.Filter(d.Deinterlace, d.ScaleTo)
	if cpuDecodeUpload && b.UploadFilter != "" {
		if filter == "" {
			filter = b.UploadFilter
		} else {
			filter = b.UploadFilter + "," + filter
		}
	}
	if filter != "" {
		args = append(args, "-vf", filter)
	}
	args = append(args, b.EncArgs(lib.ICQ, lib.Preset)...)

	args = append(args,
		"-map_metadata", "0",
		"-map_chapters", "0",
		"-metadata", ProvenanceTag+"=1",
		"-max_muxing_queue_size", "4096",
	)
	if outputFormat == "mp4" {
		args = append(args, "-movflags", "use_metadata_tags")
	}
	args = append(args, "-progress", "pipe:1", "-nostats", dst)
	return args
}

// BuildRemux copies video untouched and only rewrites the track layout.
func BuildRemux(src, dst string, info *probe.MediaInfo, d decision.Decision) []string {
	return BuildRemuxForContainer(src, dst, info, d, "matroska")
}

func BuildRemuxForContainer(src, dst string, info *probe.MediaInfo, d decision.Decision, outputFormat string) []string {
	args := []string{"-hide_banner", "-nostdin", "-y", "-i", src}
	args = append(args, "-map", fmt.Sprintf("0:%d", d.Video), "-c:v", "copy")
	args = appendAudioMaps(args, d)
	args = appendSubMaps(args, d, info, outputFormat)
	args = append(args,
		"-map_metadata", "0",
		"-map_chapters", "0",
		"-metadata", ProvenanceTag+"=1",
	)
	if outputFormat == "mp4" {
		args = append(args, "-movflags", "use_metadata_tags")
	}
	args = append(args, "-progress", "pipe:1", "-nostats", dst)
	return args
}

// appendAudioMaps emits one -map per kept track and, for converted tracks, the
// per-output-stream codec options. Indices here are output ordinals, which is
// what -c:a:N expects.
func appendAudioMaps(args []string, d decision.Decision) []string {
	for i, srcIdx := range d.AudioKeep {
		args = append(args, "-map", fmt.Sprintf("0:%d", srcIdx))
		target, convert := d.AudioTargets[srcIdx]
		ord := strconv.Itoa(i)
		if convert {
			args = append(args,
				"-c:a:"+ord, target.Codec,
				"-ac:a:"+ord, strconv.Itoa(target.Channels),
				"-b:a:"+ord, strconv.FormatInt(target.Bitrate, 10),
			)
		} else {
			args = append(args, "-c:a:"+ord, "copy")
		}
	}
	return args
}

func appendSubMaps(args []string, d decision.Decision, info *probe.MediaInfo, outputFormat string) []string {
	for ordinal, srcIdx := range d.SubsKeep {
		args = append(args, "-map", fmt.Sprintf("0:%d", srcIdx))
		codec := subtitleCodec(info, srcIdx)
		if codec == "mov_text" && outputFormat == "matroska" {
			// mov_text is MP4-only; convert the text cues to SRT for Matroska.
			args = append(args, "-c:s:"+strconv.Itoa(ordinal), "srt")
		} else {
			args = append(args, "-c:s:"+strconv.Itoa(ordinal), "copy")
		}
	}
	return args
}

func subtitleCodec(info *probe.MediaInfo, index int) string {
	for _, sub := range info.Subs {
		if sub.Index == index {
			return sub.Codec
		}
	}
	return ""
}
