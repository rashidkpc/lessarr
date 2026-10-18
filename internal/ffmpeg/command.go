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
	switch d.Action {
	case decision.Transcode:
		return BuildTranscode(b, src, dst, info, d, lib), nil
	case decision.Remux:
		return BuildRemux(src, dst, info, d), nil
	case decision.Skip:
		return nil, fmt.Errorf("no command for a skip decision")
	}
	return nil, fmt.Errorf("unknown action %q", d.Action)
}

// BuildTranscode re-encodes the video stream and applies the track plan.
func BuildTranscode(b Backend, src, dst string, info *probe.MediaInfo, d decision.Decision, lib config.Library) []string {
	args := []string{"-hide_banner", "-nostdin", "-y"}
	args = append(args, b.InputArgs...)
	args = append(args, "-i", src)

	args = append(args, "-map", fmt.Sprintf("0:%d", d.Video))
	args = appendAudioMaps(args, d)
	args = appendSubMaps(args, d)

	if filter := b.Filter(d.Deinterlace, d.ScaleTo); filter != "" {
		args = append(args, "-vf", filter)
	}
	args = append(args, b.EncArgs(lib.ICQ)...)

	args = append(args,
		"-map_metadata", "0",
		"-map_chapters", "0",
		"-metadata", ProvenanceTag+"=1",
		"-max_muxing_queue_size", "4096",
		"-progress", "pipe:1",
		"-nostats",
		dst,
	)
	return args
}

// BuildRemux copies video untouched and only rewrites the track layout.
func BuildRemux(src, dst string, info *probe.MediaInfo, d decision.Decision) []string {
	args := []string{"-hide_banner", "-nostdin", "-y", "-i", src}
	args = append(args, "-map", fmt.Sprintf("0:%d", d.Video), "-c:v", "copy")
	args = appendAudioMaps(args, d)
	args = appendSubMaps(args, d)
	args = append(args,
		"-map_metadata", "0",
		"-map_chapters", "0",
		"-metadata", ProvenanceTag+"=1",
		"-progress", "pipe:1",
		"-nostats",
		dst,
	)
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

func appendSubMaps(args []string, d decision.Decision) []string {
	for _, srcIdx := range d.SubsKeep {
		args = append(args, "-map", fmt.Sprintf("0:%d", srcIdx))
	}
	if len(d.SubsKeep) > 0 {
		args = append(args, "-c:s", "copy")
	}
	return args
}
