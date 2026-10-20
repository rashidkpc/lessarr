package analysis

import (
	"testing"

	"github.com/rashidkpc/lessarr/internal/probe"
)

func TestTypeBytes(t *testing.T) {
	info := &probe.MediaInfo{
		Video: probe.Video{Index: 0},
		Audio: []probe.Audio{{Index: 1}, {Index: 2}},
		Subs:  []probe.Sub{{Index: 3}},
	}
	packets := map[int]int64{0: 100, 1: 20, 2: 30, 3: 5}
	video, audio, subs := typeBytes(info, packets)
	if video != 100 || audio != 50 || subs != 5 {
		t.Fatalf("typeBytes = %d, %d, %d", video, audio, subs)
	}
}

func TestTotals(t *testing.T) {
	got := totals(100, 40)
	if got.Before != 100 || got.After != 40 || got.Saved != 60 {
		t.Fatalf("totals = %+v", got)
	}
}
