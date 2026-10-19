package ffmpeg

import (
	"context"
	"fmt"
	"os"
	"time"
)

type Selection struct {
	Backend        Backend
	HardwareDecode bool
	Reason         string
}

// Select tries hardware encoders before falling back to software. A compiled
// encoder is not treated as working until a short encode and decode succeeds.
func Select(ctx context.Context, configured, device, sourceCodec string, runner Runner) (Selection, error) {
	if runner.FFmpeg == "" {
		runner = NewRunner()
	}
	decoders, err := runner.Decoders(ctx)
	if err != nil {
		return Selection{}, err
	}
	hwDecode := HardwareDecodeSupport(decoders)[sourceCodec]
	if configured != "" && configured != "auto" {
		b, err := ByName(configured, device)
		if err != nil {
			return Selection{}, err
		}
		return Selection{Backend: b, HardwareDecode: hwDecode, Reason: "explicit configuration"}, nil
	}

	encoders, err := runner.Encoders(ctx)
	if err != nil {
		return Selection{}, err
	}
	work, err := os.MkdirTemp("", "lessarr-encoder-check-")
	if err != nil {
		return Selection{}, fmt.Errorf("create encoder check directory: %w", err)
	}
	defer os.RemoveAll(work)

	for _, name := range []string{"vaapi", "qsv"} {
		b, _ := ByName(name, device)
		if !encoders[b.Encoder] {
			continue
		}
		trial := runner.TrialEncode(ctx, b, device, work, 1, 30*time.Second)
		if trial.OK && trial.Decodes {
			return Selection{Backend: b, HardwareDecode: hwDecode, Reason: "hardware trial encode succeeded"}, nil
		}
	}

	if !encoders["libx265"] {
		return Selection{}, fmt.Errorf("no working hardware HEVC encoder and libx265 is unavailable")
	}
	b := Software()
	trial := runner.TrialEncode(ctx, b, device, work, 1, 2*time.Minute)
	if !trial.OK || !trial.Decodes {
		return Selection{}, fmt.Errorf("software encoder trial failed: %s", trial.Err)
	}
	return Selection{Backend: b, HardwareDecode: false, Reason: "hardware trials failed; using software"}, nil
}
