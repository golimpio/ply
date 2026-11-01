package stream

import (
	"math"
	"strconv"
)

// TimelineOffset is added to every output timestamp. Streams whose first
// DTS is negative (B-frames, AAC priming) would otherwise produce negative
// fragment decode times, which are unsigned in fMP4.
const TimelineOffset = 2.0

// gopPattern and gopFile name the per-GOP fragments ffmpeg writes.
const gopPattern = "gop%05d.m4s"

// VideoSpec says how the video stream is produced. The zero value copies it.
// M4 fills Encode (and Pre for hardware decode flags) for transcodes.
type VideoSpec struct {
	Codec  string   // source codec; hevc copies are tagged hvc1
	Encode []string // output arguments replacing "-c:v copy" for a transcode
	Pre    []string // input-side arguments (hwaccel flags), placed before -i
}

func (v VideoSpec) copy() bool { return len(v.Encode) == 0 }

// AudioSpec says how the audio stream is produced.
type AudioSpec struct {
	Codec string // source codec
	Copy  bool   // otherwise AAC 192k stereo
}

// RunSpec describes one ffmpeg invocation: produce GOP fragments from the
// keyframe StartGOP onwards.
type RunSpec struct {
	Input      string
	VideoIndex int // ffprobe stream index
	AudioIndex int // ffprobe stream index, <0 for none
	Video      VideoSpec
	Audio      AudioSpec
	SeekSec    float64 // input seek (see SeekTarget); <=0 starts at the beginning
	StartGOP   int
	InitName   string // per-run init file, so a restart never rewrites the served one
	Offset     float64
}

// BuildArgs returns the ffmpeg arguments for r. Output goes to the working
// directory: InitName, gop%05d.m4s (numbered from StartGOP) and ffmpeg.m3u8.
//
// Timestamps are the source's (-copyts) shifted so the file starts at 0 and
// then by Offset, whatever the seek position, so fragments from different
// runs line up. -hls_time is tiny so that every keyframe starts a new
// fragment: ffmpeg's own notion of segment length is irrelevant here.
// movflags=frag_discont makes the first fragment of a run carry its true
// decode time instead of 0; the trailing "+" matters, hlsenc appends its own
// flags without a separator.
func BuildArgs(r RunSpec) []string {
	a := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	a = append(a, r.Video.Pre...)
	if r.SeekSec > 0 {
		a = append(a, "-ss", seconds(r.SeekSec))
	}
	a = append(a, "-i", r.Input, "-map", "0:"+strconv.Itoa(r.VideoIndex))
	if r.AudioIndex >= 0 {
		a = append(a, "-map", "0:"+strconv.Itoa(r.AudioIndex))
	}

	if r.Video.copy() {
		a = append(a, "-c:v", "copy")
		if r.Video.Codec == "hevc" {
			a = append(a, "-tag:v", "hvc1")
		}
	} else {
		a = append(a, r.Video.Encode...)
	}
	switch {
	case r.AudioIndex < 0:
		a = append(a, "-an")
	case r.Audio.Copy:
		a = append(a, "-c:a", "copy")
		if r.Audio.Codec == "aac" {
			a = append(a, "-bsf:a", "aac_adtstoasc") // ADTS (ts) to MP4 framing; a no-op otherwise
		}
	default:
		a = append(a, "-c:a", "aac", "-b:a", "192k", "-ac", "2")
	}
	a = append(a, "-sn", "-dn", "-max_muxing_queue_size", "4096")

	a = append(a,
		"-copyts", "-start_at_zero", "-avoid_negative_ts", "disabled",
		"-output_ts_offset", seconds(r.Offset),
		"-f", "hls", "-hls_time", "0.001", "-hls_list_size", "0",
		"-hls_segment_type", "fmp4", "-hls_fmp4_init_filename", r.InitName,
		"-start_number", strconv.Itoa(r.StartGOP),
		"-hls_segment_filename", gopPattern,
		"-hls_flags", "temp_file",
		"-hls_segment_options", "movflags=frag_discont+skip_sidx+",
		"ffmpeg.m3u8")
	return a
}

func seconds(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

// SeekTarget returns the -ss position that makes ffmpeg start at keyframe g
// of kf (seconds from the start of the file); 0 means "from the beginning".
//
// Seekable containers land on the last keyframe at or before the target, but
// ffmpeg first moves the target back by 3/23 s when the video has frame
// reordering, so aim past that. MPEG-TS cannot seek to a keyframe: it lands
// somewhere before the target and ffmpeg then skips to the next keyframe, so
// aim just before the keyframe instead.
func SeekTarget(container string, kf []float64, g int) float64 {
	if g <= 0 || g >= len(kf) {
		return 0
	}
	t := kf[g]
	if container == "ts" {
		return math.Max(t-0.05, 0.001)
	}
	bias := 0.14
	if g+1 < len(kf) && kf[g+1]-t <= 0.15 {
		bias = 0.01
	}
	return t + bias
}
