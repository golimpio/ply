package hwaccel

import (
	"fmt"
	"strconv"
	"strings"
)

// Request describes the video to transcode.
type Request struct {
	Codec         string // source video codec (ffprobe name)
	BitDepth      int    // source bits per component, 0 if unknown
	Width, Height int    // source size, 0 if unknown
	HDR           bool   // PQ or HLG transfer
	VideoIndex    int    // ffprobe stream index
	BurnSubtitle  int    // ffprobe index of an image subtitle to burn in, <0 for none
	MaxHeight     int    // profile limit, 0 for none
}

// Spec is the video half of an ffmpeg command line.
type Spec struct {
	Encoder string   // ffmpeg encoder name
	Pre     []string // input options (hwaccel flags), before -i
	Filter  string   // -vf chain; empty for none
	// Complex is a -filter_complex graph whose output is labelled [v], used
	// when a subtitle is burned in; Filter is then empty.
	Complex string
	Encode  []string // output options: codec, rate control, profile
	// GPU is true when decode, scale and encode all stay on the GPU.
	GPU     bool
	Height  int // output height, 0 when the source size is unknown
	Bitrate int // bits per second cap
	// Reasons say what is being done and why a cheaper path was not taken.
	Reasons []string
}

// Ladder is the video bitrate cap, in bits per second, for an output height.
func Ladder(height int) int {
	switch {
	case height <= 0:
		return 8_000_000
	case height <= 360:
		return 1_000_000
	case height <= 480:
		return 2_000_000
	case height <= 720:
		return 4_000_000
	case height <= 1080:
		return 8_000_000
	case height <= 1440:
		return 12_000_000
	}
	return 20_000_000
}

// hwaccelName is the decode method ffmpeg lists for each encoder family.
func (k Kind) hwaccelName() string {
	switch k {
	case NVENC:
		return "cuda"
	case Software:
		return ""
	}
	return string(k)
}

// hwDecodable reports whether the GPU decoders are trusted with codec. Wider
// support exists but varies by generation; everything else decodes in
// software and still gets a GPU encode.
func hwDecodable(codec string) bool { return codec == "h264" || codec == "hevc" }

// gpuScale is the scale-and-convert filter of each family and the ffmpeg
// filter it needs.
func gpuScale(k Kind, w, h int, resize bool) (name, filter string) {
	size := func() string {
		if !resize {
			return ""
		}
		return fmt.Sprintf("w=%d:h=%d:", w, h)
	}
	switch k {
	case VideoToolbox:
		if !resize {
			return "scale_vt", "" // decode output feeds the encoder directly
		}
		return "scale_vt", fmt.Sprintf("scale_vt=w=%d:h=%d", w, h)
	case VAAPI:
		return "scale_vaapi", "scale_vaapi=" + size() + "format=nv12"
	case QSV:
		return "vpp_qsv", "vpp_qsv=" + size() + "format=nv12"
	case NVENC:
		return "scale_cuda", "scale_cuda=" + size() + "format=nv12"
	}
	return "", ""
}

// Plan builds the video arguments for r with the encoder in i.
func (i Info) Plan(r Request) Spec {
	enc := i.encoder()
	outW, outH, resize := target(r)
	rate := Ladder(outH)
	s := Spec{Encoder: enc.Name, Height: outH, Bitrate: rate}
	say := func(format string, args ...any) { s.Reasons = append(s.Reasons, fmt.Sprintf(format, args...)) }

	burn := r.BurnSubtitle >= 0
	tonemap := r.HDR && i.Filters["zscale"] && i.Filters["tonemap"]
	tenBit := r.BitDepth > 8

	// Can decode and scale stay on the GPU?
	var soft string // why not; empty means GPU end to end
	switch {
	case !enc.Hardware():
	case burn:
		soft = "subtitle burn-in needs software decode, scale and overlay"
	case tonemap:
		soft = "HDR tone mapping needs software decode and scale"
	case !hwDecodable(r.Codec):
		soft = fmt.Sprintf("%s is not decoded on the GPU", r.Codec)
	case !i.HWAccels[enc.Kind.hwaccelName()]:
		soft = fmt.Sprintf("ffmpeg has no %s hwaccel", enc.Kind.hwaccelName())
	default:
		if name, f := gpuScale(enc.Kind, outW, outH, resize); name != "" && f != "" && !i.Filters[name] {
			soft = fmt.Sprintf("ffmpeg has no %s filter", name)
		}
	}

	switch {
	case !enc.Hardware():
		say("video transcoded to h264 with %s (software decode and scale)", enc.Name)
		s.Filter = softChain(enc, r, outH, resize, tonemap)
	case soft == "":
		say("video transcoded to h264 with %s (decode, scale and encode on the GPU)", enc.Name)
		s.GPU = true
		s.Pre = gpuDecodeArgs(enc)
		_, s.Filter = gpuScale(enc.Kind, outW, outH, resize)
	default:
		say("video transcoded to h264 with %s (%s, so decode and scale run in software)", enc.Name, soft)
		s.Pre = uploadDeviceArgs(enc)
		s.Filter = softChain(enc, r, outH, resize, tonemap)
	}

	if burn {
		s.Complex = burnGraph(enc, r, outH, resize, tonemap, i.Filters[RefScale])
		s.Filter = ""
	}

	if resize {
		say("scaled from %dp to %dp", r.Height, outH)
	}
	say("bitrate capped at %d Mbps", rate/1_000_000)
	switch {
	case r.HDR && tonemap:
		say("HDR tone-mapped to SDR (zscale + tonemap)")
	case r.HDR:
		say("HDR converted to SDR by plain format conversion, no tone mapping (ffmpeg lacks zscale/tonemap): colours will look washed out")
	case tenBit:
		say("%d-bit video converted to 8-bit", r.BitDepth)
	}
	s.Encode = encodeArgs(enc, rate)
	return s
}

// target is the output size: the source scaled down to MaxHeight when it is
// taller. resize is false when the source already fits.
func target(r Request) (w, h int, resize bool) {
	h = r.Height
	if r.MaxHeight > 0 && (r.Height == 0 || r.Height > r.MaxHeight) {
		h, resize = max(r.MaxHeight&^1, 2), true // even: a custom profile may say 361
	}
	if !resize {
		return r.Width, r.Height, false
	}
	if r.Width > 0 && r.Height > 0 {
		w = int(float64(r.Width)*float64(h)/float64(r.Height)+0.5) &^ 1
	} else {
		w = -2
		if r.Height == 0 {
			h = max(r.MaxHeight&^1, 2) // unknown source: the filter uses min(ih, ...)
		}
	}
	return w, h, true
}

// gpuDecodeArgs keep decoded frames in GPU memory so the scale filter and
// encoder consume them directly (no hwdownload).
func gpuDecodeArgs(e Encoder) []string {
	switch e.Kind {
	case VideoToolbox:
		return []string{"-hwaccel", "videotoolbox", "-hwaccel_output_format", "videotoolbox_vld"}
	case VAAPI:
		return []string{"-hwaccel", "vaapi", "-hwaccel_device", e.Device, "-hwaccel_output_format", "vaapi"}
	case QSV:
		return []string{"-hwaccel", "qsv", "-hwaccel_device", e.Device, "-hwaccel_output_format", "qsv"}
	case NVENC:
		return []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"}
	}
	return nil
}

// uploadDeviceArgs declare the device the encoder needs when software frames
// are uploaded to it (VAAPI and QSV encoders only take GPU frames).
func uploadDeviceArgs(e Encoder) []string {
	switch e.Kind {
	case VAAPI:
		return []string{"-init_hw_device", "vaapi=va:" + e.Device, "-filter_hw_device", "va"}
	case QSV:
		return []string{"-init_hw_device", "qsv=hw", "-filter_hw_device", "hw"}
	}
	return nil
}

// scaleFilter is the software scale to outH, or the trim to even dimensions,
// "" when the source already fits and is even. libx264 (and 4:2:0 in general)
// refuses an odd width or height: "height not divisible by 2".
func scaleFilter(r Request, outH int, resize bool) string {
	switch {
	case resize && r.Height > 0:
		return fmt.Sprintf("scale=-2:%d", outH) // -2: the width follows, rounded to even
	case resize:
		return fmt.Sprintf("scale=-2:'trunc(min(ih,%d)/2)*2'", outH)
	case r.Width%2 != 0 || r.Height%2 != 0:
		return "scale=trunc(iw/2)*2:trunc(ih/2)*2"
	}
	return ""
}

// colorChain converts to what the encoder takes: optional tone mapping, 8-bit
// 4:2:0 and, for VAAPI and QSV (which only encode GPU frames), the upload.
func colorChain(enc Encoder, tonemap bool) []string {
	var f []string
	if tonemap {
		f = append(f, "zscale=t=linear:npl=100", "format=gbrpf32le", "zscale=p=bt709",
			"tonemap=tonemap=hable:desat=0", "zscale=t=bt709:m=bt709:r=tv")
	}
	switch enc.Kind {
	case VAAPI:
		f = append(f, "format=nv12", "hwupload")
	case QSV:
		f = append(f, "format=nv12", "hwupload=extra_hw_frames=64")
	default:
		f = append(f, "format=yuv420p")
	}
	return f
}

// softChain is the whole software filter chain: scale, then colorChain.
func softChain(enc Encoder, r Request, outH int, resize, tonemap bool) string {
	var f []string
	if sc := scaleFilter(r, outH, resize); sc != "" {
		f = append(f, sc)
	}
	return strings.Join(append(f, colorChain(enc, tonemap)...), ",")
}

// burnGraph overlays subtitle stream r.BurnSubtitle on the video. The
// subtitle bitmaps are drawn for the source's canvas (often 1920x1080 even on
// 4K video, or 720x480 on an HD one), so they are scaled to the size of the
// already scaled video first. refScale says whether this ffmpeg's scale filter
// takes a reference input; if not, the older scale2ref does the same.
func burnGraph(enc Encoder, r Request, outH int, resize, tonemap, refScale bool) string {
	video := fmt.Sprintf("[0:%d]", r.VideoIndex)
	sub := fmt.Sprintf("[0:%d]", r.BurnSubtitle)
	sc := scaleFilter(r, outH, resize)
	var g []string
	if refScale {
		// scale=rw:rh with two inputs scales the first to the size of the second
		// and consumes it, so the video is split: one copy to measure by, one to
		// draw on.
		chain := "split=2[ref][vref]"
		if sc != "" {
			chain = sc + "," + chain
		}
		g = append(g, video+chain, sub+"[vref]scale=w=rw:h=rh[sub]")
	} else {
		if sc != "" {
			g = append(g, video+sc+"[m]")
			video = "[m]"
		}
		// In scale2ref, iw:ih are the reference's size and main_w:main_h the size
		// of the input being scaled (here the subtitle canvas): the other way
		// round from what the names suggest, and main_w:main_h scales nothing.
		g = append(g, sub+video+"scale2ref=w=iw:h=ih[sub][ref]")
	}
	g = append(g, "[ref][sub]overlay=eof_action=pass,"+strings.Join(colorChain(enc, tonemap), ",")+"[v]")
	return strings.Join(g, ";")
}

// encodeArgs are the encoder options: H.264 High, bitrate capped at rate, and
// no keyframes of the encoder's own — the only keyframes are the ones
// -force_key_frames asks for, which is what keeps segments equal.
func encodeArgs(e Encoder, rate int) []string {
	r := strconv.Itoa(rate)
	buf := strconv.Itoa(rate * 2)
	const noGOP = "100000" // max keyframe interval: never reached
	switch e.Kind {
	case VideoToolbox:
		return []string{"-c:v", e.Name, "-profile:v", "high", "-b:v", r, "-maxrate", r, "-g", noGOP}
	case VAAPI:
		return []string{"-c:v", e.Name, "-profile:v", "high", "-b:v", r, "-maxrate", r, "-bufsize", buf, "-g", noGOP}
	case QSV:
		return []string{"-c:v", e.Name, "-profile:v", "high", "-preset", "veryfast", "-b:v", r, "-maxrate", r,
			"-bufsize", buf, "-g", noGOP, "-forced_idr", "1"}
	case NVENC:
		return []string{"-c:v", e.Name, "-profile:v", "high", "-preset", "p4", "-rc", "vbr", "-cq", "23", "-b:v", r,
			"-maxrate", r, "-bufsize", buf, "-g", noGOP, "-forced-idr", "1", "-no-scenecut", "1"}
	}
	return []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "21", "-maxrate", r, "-bufsize", buf,
		"-profile:v", "high", "-g", noGOP, "-sc_threshold", "0", "-forced-idr", "1"}
}
