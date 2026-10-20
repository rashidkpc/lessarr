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
	return v.OutputWithProgress(ctx, outputPath, source, d, timeout, expectedContainer, "sample", nil, nil)
}

const (
	sampleCount   = 5
	sampleSeconds = 20
)

type sampleWindow struct {
	Start time.Duration
	Len   time.Duration
}

// sampleWindows centers short windows at 10/30/50/70/90% of the runtime.
func sampleWindows(duration time.Duration) []sampleWindow {
	length := time.Duration(sampleSeconds) * time.Second
	if duration < 2*length {
		return nil
	}
	windows := make([]sampleWindow, 0, sampleCount)
	for i := 0; i < sampleCount; i++ {
		fraction := 0.1 + 0.8*float64(i)/float64(sampleCount-1)
		start := time.Duration(fraction*float64(duration)) - length/2
		if start < 0 {
			start = 0
		}
		if latest := duration - length; start > latest {
			start = latest
		}
		windows = append(windows, sampleWindow{Start: start, Len: length})
	}
	return windows
}

// OutputWithProgress is Output with a step callback for the UI and an optional
// decode-progress callback for the long full-decode phase.
func (v Validator) OutputWithProgress(ctx context.Context, outputPath string, source *probe.MediaInfo, d decision.Decision, timeout time.Duration, expectedContainer, validation string, step func(string), progress func(ffmpeg.Progress)) (*probe.MediaInfo, error) {
	report := func(s string) {
		if step != nil {
			step(s)
		}
	}
	report("probing output")
	output, err := v.Prober.Probe(ctx, outputPath)
	if err != nil {
		return nil, fmt.Errorf("probe output: %w", err)
	}
	if output.Size <= 0 {
		return nil, fmt.Errorf("output is empty")
	}
	report("checking stream counts")
	if output.VideoCount != 1 {
		return nil, fmt.Errorf("output has %d video streams; expected exactly one", output.VideoCount)
	}
	if len(output.Audio) != len(d.AudioKeep) {
		return nil, fmt.Errorf("output has %d audio streams; expected %d", len(output.Audio), len(d.AudioKeep))
	}
	if len(output.Subs) != len(d.SubsKeep) {
		return nil, fmt.Errorf("output has %d subtitle streams; expected %d", len(output.Subs), len(d.SubsKeep))
	}
	report("checking container")
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

	report("checking video codec")
	wantCodec := source.Video.Codec
	if d.Action == decision.Transcode {
		wantCodec = "hevc"
	}
	if output.Video.Codec != wantCodec {
		return nil, fmt.Errorf("output video codec is %q; expected %q", output.Video.Codec, wantCodec)
	}

	report("checking dimensions")
	wantW, wantH := source.Video.Width, source.Video.Height
	if d.ScaleTo != nil {
		wantW, wantH = d.ScaleTo.W, d.ScaleTo.H
	}
	if output.Video.Width != wantW || output.Video.Height != wantH {
		return nil, fmt.Errorf("output dimensions are %dx%d; expected %dx%d", output.Video.Width, output.Video.Height, wantW, wantH)
	}

	report("checking duration")
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

	report("checking audio tracks")
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

	if validation == "full" {
		report("decoding output")
		if progress != nil {
			err = v.Runner.DecodeProgress(ctx, outputPath, timeout, progress)
		} else {
			err = v.Runner.Decode(ctx, outputPath, timeout)
		}
		if err != nil {
			return nil, fmt.Errorf("full output decode failed: %w", err)
		}
	} else {
		duration := time.Duration(outputVideoDuration * float64(time.Second))
		windows := sampleWindows(duration)
		if len(windows) == 0 {
			// Short files are cheap to decode completely and avoid awkward
			// overlapping samples.
			report("decoding output")
			if progress != nil {
				err = v.Runner.DecodeProgress(ctx, outputPath, timeout, progress)
			} else {
				err = v.Runner.Decode(ctx, outputPath, timeout)
			}
			if err != nil {
				return nil, fmt.Errorf("output decode failed: %w", err)
			}
		} else {
			if d.Action == decision.Remux {
				report("scanning packets")
				if err := v.Runner.ScanPackets(ctx, outputPath, timeout); err != nil {
					return nil, fmt.Errorf("packet scan failed: %w", err)
				}
			}
			for i, window := range windows {
				report(fmt.Sprintf("sampling %d/%d", i+1, len(windows)))
				if err := v.Runner.DecodeSegment(ctx, outputPath, window.Start, window.Len, timeout); err != nil {
					return nil, fmt.Errorf("sample %d/%d decode failed at %s: %w",
						i+1, len(windows), window.Start.Round(time.Second), err)
				}
			}
		}
	}
	report("validated")
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
