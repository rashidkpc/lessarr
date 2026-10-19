// Package validate verifies an encoded/remuxed temp file before it can replace
// the source. Validation is structural plus a full decode; it never decides
// whether the chosen quality is perceptually good.
package validate

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/rashidkpc/lessarr/internal/decision"
	"github.com/rashidkpc/lessarr/internal/ffmpeg"
	"github.com/rashidkpc/lessarr/internal/probe"
)

type Validator struct {
	Prober probe.Prober
	Runner ffmpeg.Runner
}

// Output probes the output, checks it against the decision plan and source,
// then fully decodes it. Call this only after ffmpeg exits successfully.
func (v Validator) Output(ctx context.Context, outputPath string, source *probe.MediaInfo, d decision.Decision, timeout time.Duration, expectedContainer string) (*probe.MediaInfo, error) {
	output, err := v.Prober.Probe(ctx, outputPath)
	if err != nil {
		return nil, fmt.Errorf("probe output: %w", err)
	}
	if output.Size <= 0 {
		return nil, fmt.Errorf("output is empty")
	}
	if output.VideoCount != 1 {
		return nil, fmt.Errorf("output has %d video streams; expected exactly one", output.VideoCount)
	}
	if len(output.Audio) != len(d.AudioKeep) {
		return nil, fmt.Errorf("output has %d audio streams; expected %d", len(output.Audio), len(d.AudioKeep))
	}
	if len(output.Subs) != len(d.SubsKeep) {
		return nil, fmt.Errorf("output has %d subtitle streams; expected %d", len(output.Subs), len(d.SubsKeep))
	}
	switch expectedContainer {
	case "matroska":
		if !output.IsMatroska() {
			return nil, fmt.Errorf("output container is %q; expected Matroska", output.Container)
		}
	case "mp4":
		if !strings.Contains(output.Container, "mp4") && !strings.Contains(output.Container, "mov") {
			return nil, fmt.Errorf("output container is %q; expected MP4", output.Container)
		}
	default:
		return nil, fmt.Errorf("unsupported expected container %q", expectedContainer)
	}

	wantCodec := source.Video.Codec
	if d.Action == decision.Transcode {
		wantCodec = "hevc"
	}
	if output.Video.Codec != wantCodec {
		return nil, fmt.Errorf("output video codec is %q; expected %q", output.Video.Codec, wantCodec)
	}

	wantW, wantH := source.Video.Width, source.Video.Height
	if d.ScaleTo != nil {
		wantW, wantH = d.ScaleTo.W, d.ScaleTo.H
	}
	if output.Video.Width != wantW || output.Video.Height != wantH {
		return nil, fmt.Errorf("output dimensions are %dx%d; expected %dx%d", output.Video.Width, output.Video.Height, wantW, wantH)
	}

	sourceVideoDuration := source.Video.Duration
	if sourceVideoDuration <= 0 {
		sourceVideoDuration = source.Duration
	}
	outputVideoDuration := output.Video.Duration
	if outputVideoDuration <= 0 {
		outputVideoDuration = output.Duration
	}
	if sourceVideoDuration > 0 && outputVideoDuration > 0 {
		tolerance := math.Max(2, sourceVideoDuration*0.005)
		if delta := math.Abs(outputVideoDuration - sourceVideoDuration); delta > tolerance {
			return nil, fmt.Errorf("duration changed by %.3fs (tolerance %.3fs)", delta, tolerance)
		}
	}

	for i, sourceIndex := range d.AudioKeep {
		sourceAudio, ok := audioByIndex(source.Audio, sourceIndex)
		if !ok {
			return nil, fmt.Errorf("decision references missing source audio stream %d", sourceIndex)
		}
		want := sourceAudio.Codec
		if target, convert := d.AudioTargets[sourceIndex]; convert {
			want = target.Codec
		}
		if output.Audio[i].Codec != want {
			return nil, fmt.Errorf("output audio stream %d codec is %q; expected %q", i, output.Audio[i].Codec, want)
		}
	}

	if err := v.Runner.Decode(ctx, outputPath, timeout); err != nil {
		return nil, fmt.Errorf("full output decode failed: %w", err)
	}
	return output, nil
}

func audioByIndex(tracks []probe.Audio, index int) (probe.Audio, bool) {
	for _, track := range tracks {
		if track.Index == index {
			return track, true
		}
	}
	return probe.Audio{}, false
}
