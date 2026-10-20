// Package analysis attributes a completed job's size change to video, audio,
// subtitles, and container overhead, and measures sampled SSIM against the
// retained original.
package analysis

import (
	"context"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/rashidkpc/lessarr/internal/ffmpeg"
	"github.com/rashidkpc/lessarr/internal/probe"
)

type Totals struct {
	Before int64 `json:"before"`
	After  int64 `json:"after"`
	Saved  int64 `json:"saved"`
}

type Sample struct {
	AtSeconds float64 `json:"at_seconds"`
	SSIM      float64 `json:"ssim"`
}

type Result struct {
	Video     Totals   `json:"video"`
	Audio     Totals   `json:"audio"`
	Subtitles Totals   `json:"subtitles"`
	Other     Totals   `json:"other"`
	Total     Totals   `json:"total"`
	SSIM      []Sample `json:"ssim,omitempty"`
	SSIMError string   `json:"ssim_error,omitempty"`
}

type Prober interface {
	PacketBytes(context.Context, string) (map[int]int64, error)
	Probe(context.Context, string) (*probe.MediaInfo, error)
}

var ssimAll = regexp.MustCompile(`All:([0-9]+(?:\.[0-9]+)?)`)

// Compare measures the source (retained original) and output stream bytes, then
// samples SSIM at 10/50/90%. Packet scans are exact but full-file demuxes.
func Compare(ctx context.Context, prober Prober, runner ffmpeg.Runner, sourcePath, outputPath string, source, output *probe.MediaInfo) (Result, error) {
	srcPackets, err := prober.PacketBytes(ctx, sourcePath)
	if err != nil {
		return Result{}, err
	}
	outPackets, err := prober.PacketBytes(ctx, outputPath)
	if err != nil {
		return Result{}, err
	}
	srcVideo, srcAudio, srcSubs := typeBytes(source, srcPackets)
	outVideo, outAudio, outSubs := typeBytes(output, outPackets)
	srcTotal, err := os.Stat(sourcePath)
	if err != nil {
		return Result{}, err
	}
	outTotal, err := os.Stat(outputPath)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		Video:     totals(srcVideo, outVideo),
		Audio:     totals(srcAudio, outAudio),
		Subtitles: totals(srcSubs, outSubs),
		Total:     totals(srcTotal.Size(), outTotal.Size()),
	}
	result.Other = Totals{
		Before: result.Total.Before - srcVideo - srcAudio - srcSubs,
		After:  result.Total.After - outVideo - outAudio - outSubs,
		Saved:  result.Total.Saved - result.Video.Saved - result.Audio.Saved - result.Subtitles.Saved,
	}
	result.SSIM, result.SSIMError = sampleSSIM(ctx, runner, sourcePath, outputPath, source, output)
	return result, nil
}

func totals(before, after int64) Totals {
	return Totals{Before: before, After: after, Saved: before - after}
}

func typeBytes(info *probe.MediaInfo, packets map[int]int64) (video, audio, subtitles int64) {
	if info == nil {
		return 0, 0, 0
	}
	video = packets[info.Video.Index]
	for _, track := range info.Audio {
		audio += packets[track.Index]
	}
	for _, track := range info.Subs {
		subtitles += packets[track.Index]
	}
	return video, audio, subtitles
}

func sampleSSIM(ctx context.Context, runner ffmpeg.Runner, sourcePath, outputPath string, source, output *probe.MediaInfo) ([]Sample, string) {
	duration := source.Duration
	if output.Duration > 0 && (duration <= 0 || output.Duration < duration) {
		duration = output.Duration
	}
	if duration <= 0 {
		return nil, "duration is unknown"
	}
	length := 20.0
	if duration < 2*length {
		length = duration
	}
	var windows []float64
	if duration < 2*length {
		windows = []float64{0}
	} else {
		windows = []float64{0.1, 0.5, 0.9}
	}
	var samples []Sample
	for _, fraction := range windows {
		start := fraction*duration - length/2
		if start < 0 {
			start = 0
		}
		if latest := duration - length; start > latest {
			start = latest
		}
		graph := "[0:v:0]format=yuv420p[a];[1:v:0]format=yuv420p[b];[a][b]ssim"
		log, err := runner.RunCapture(ctx, 10*time.Minute,
			"-hide_banner", "-nostdin", "-v", "info", "-nostats",
			"-ss", seconds(start), "-t", seconds(length), "-i", sourcePath,
			"-ss", seconds(start), "-t", seconds(length), "-i", outputPath,
			"-lavfi", graph, "-f", "null", "-",
		)
		if err != nil {
			return samples, fmt.Sprintf("sample at %ds: %v", int(start), err)
		}
		match := ssimAll.FindStringSubmatch(log)
		if len(match) != 2 {
			return samples, fmt.Sprintf("sample at %ds: no SSIM value", int(start))
		}
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return samples, fmt.Sprintf("sample at %ds: invalid SSIM", int(start))
		}
		samples = append(samples, Sample{AtSeconds: start, SSIM: value})
	}
	return samples, ""
}

func seconds(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }
