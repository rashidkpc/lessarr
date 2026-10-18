package decision

import "github.com/rashidkpc/lessarr/internal/probe"

// resBucket groups resolutions for threshold lookup. Bucketing by pixel area
// rather than height keeps 2.40:1 content in the right class.
type resBucket int

const (
	b480 resBucket = iota
	b720
	b1080
	b2160
)

func bucketFor(m *probe.MediaInfo) resBucket {
	area := m.Video.Width * m.Video.Height
	switch {
	case area <= 720*576:
		return b480
	case area <= 1280*720:
		return b720
	case area <= 1920*1080:
		return b1080
	default:
		return b2160
	}
}

// hevcWasteful is the bpp at which an already-efficient codec is worth
// measuring, for sources we would otherwise leave alone.
var hevcWasteful = map[resBucket]float64{
	b480: 0.12, b720: 0.09, b1080: 0.08, b2160: 0.06,
}

// floorFor is the bpp below which a source is already so compressed that
// re-encoding would compound artifacts rather than remove waste.
var floorFor = map[resBucket]float64{
	b480: 0.05, b720: 0.04, b1080: 0.03, b2160: 0.025,
}

func wastefulFor(m *probe.MediaInfo) float64    { return hevcWasteful[bucketFor(m)] }
func floorForSource(m *probe.MediaInfo) float64 { return floorFor[bucketFor(m)] }

// codecGap is the fraction of video bitrate a codec-generation change is
// expected to remove at equal perceptual quality. Deliberately conservative.
func codecGap(codec string) float64 {
	switch codec {
	case "h264":
		return 0.50
	case "mpeg2video", "vc1", "mpeg4", "msmpeg4v3", "msmpeg4v2", "wmv3", "xvid":
		return 0.75
	}
	return 0.60
}
