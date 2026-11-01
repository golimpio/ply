package hwaccel

import (
	"slices"
	"strings"
	"testing"
)

func info(enc Encoder, filters ...string) Info {
	i := Info{Encoder: enc, Filters: map[string]bool{}, HWAccels: map[string]bool{
		"videotoolbox": true, "vaapi": true, "qsv": true, "cuda": true,
	}}
	for _, f := range filters {
		i.Filters[f] = true
	}
	return i
}

var (
	vt   = Encoder{Kind: VideoToolbox, Name: "h264_videotoolbox"}
	va   = Encoder{Kind: VAAPI, Name: "h264_vaapi", Device: DefaultVAAPIDevice}
	qsv  = Encoder{Kind: QSV, Name: "h264_qsv", Device: DefaultVAAPIDevice}
	nv   = Encoder{Kind: NVENC, Name: "h264_nvenc"}
	x264 = SoftwareEncoder()
)

func uhd10() Request {
	return Request{Codec: "hevc", BitDepth: 10, Width: 3840, Height: 2160, VideoIndex: 0, BurnSubtitle: -1, MaxHeight: 1080}
}

func hasReason(s Spec, sub string) bool {
	return strings.Contains(strings.Join(s.Reasons, "\n"), sub)
}

func TestPlanGPUEndToEnd(t *testing.T) {
	tests := []struct {
		name   string
		info   Info
		req    func(Request) Request
		pre    string
		filter string
	}{
		{"videotoolbox scales on the GPU", info(vt, "scale_vt"), nil,
			"-hwaccel videotoolbox -hwaccel_output_format videotoolbox_vld", "scale_vt=w=1920:h=1080"},
		{"videotoolbox needs no filter when nothing is scaled", info(vt, "scale_vt"),
			func(r Request) Request { r.MaxHeight = 0; return r },
			"-hwaccel videotoolbox -hwaccel_output_format videotoolbox_vld", ""},
		{"vaapi", info(va, "scale_vaapi"), nil,
			"-hwaccel vaapi -hwaccel_device /dev/dri/renderD128 -hwaccel_output_format vaapi",
			"scale_vaapi=w=1920:h=1080:format=nv12"},
		{"vaapi converts 10-bit even when not scaling", info(va, "scale_vaapi"),
			func(r Request) Request { r.MaxHeight = 0; return r },
			"-hwaccel vaapi -hwaccel_device /dev/dri/renderD128 -hwaccel_output_format vaapi", "scale_vaapi=format=nv12"},
		{"qsv", info(qsv, "vpp_qsv"), nil,
			"-hwaccel qsv -hwaccel_device /dev/dri/renderD128 -hwaccel_output_format qsv", "vpp_qsv=w=1920:h=1080:format=nv12"},
		{"nvenc", info(nv, "scale_cuda"), nil,
			"-hwaccel cuda -hwaccel_output_format cuda", "scale_cuda=w=1920:h=1080:format=nv12"},
		{"odd aspect ratios get even widths", info(vt, "scale_vt"),
			func(r Request) Request { r.Width, r.Height = 3840, 1608; r.MaxHeight = 800; return r },
			"-hwaccel videotoolbox -hwaccel_output_format videotoolbox_vld", "scale_vt=w=1910:h=800"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := uhd10()
			if tt.req != nil {
				r = tt.req(r)
			}
			s := tt.info.Plan(r)
			if !s.GPU || strings.Join(s.Pre, " ") != tt.pre || s.Filter != tt.filter || s.Complex != "" {
				t.Errorf("GPU=%v pre=%q filter=%q complex=%q", s.GPU, strings.Join(s.Pre, " "), s.Filter, s.Complex)
			}
			for _, bad := range []string{"hwdownload", "hwupload"} {
				if strings.Contains(s.Filter, bad) {
					t.Errorf("GPU path must not %s: %q", bad, s.Filter)
				}
			}
			if !hasReason(s, "on the GPU") {
				t.Errorf("reasons = %q", s.Reasons)
			}
			if s.Encoder != tt.info.Encoder.Name || s.Encode[0] != "-c:v" || s.Encode[1] != s.Encoder {
				t.Errorf("encode = %v", s.Encode)
			}
		})
	}
}

func TestPlanSoftwareFallbacks(t *testing.T) {
	tonemapFilters := []string{"scale_vt", "zscale", "tonemap"}
	hdr := func(r Request) Request { r.HDR = true; return r }
	tests := []struct {
		name    string
		info    Info
		req     func(Request) Request
		pre     string
		filter  string
		reasons []string // all must appear
		absent  []string // none may appear
	}{
		{"libx264 decodes and scales in software", info(x264), nil, "",
			"scale=-2:1080,format=yuv420p", []string{"libx264 (software decode and scale)", "scaled from 2160p to 1080p", "10-bit video converted to 8-bit"}, nil},
		{"vp9 is not decoded on the GPU but still encodes there", info(vt, "scale_vt"),
			func(r Request) Request { r.Codec = "vp9"; r.BitDepth = 8; return r }, "",
			"scale=-2:1080,format=yuv420p", []string{"vp9 is not decoded on the GPU, so decode and scale run in software"}, []string{"on the GPU)"}},
		{"missing hwaccel", func() Info { i := info(vt, "scale_vt"); i.HWAccels = nil; return i }(), nil, "",
			"scale=-2:1080,format=yuv420p", []string{"ffmpeg has no videotoolbox hwaccel"}, nil},
		{"missing scale filter", info(vt), nil, "",
			"scale=-2:1080,format=yuv420p", []string{"ffmpeg has no scale_vt filter"}, nil},
		{"hdr is tone-mapped in software", info(vt, tonemapFilters...), hdr, "",
			"scale=-2:1080,zscale=t=linear:npl=100,format=gbrpf32le,zscale=p=bt709,tonemap=tonemap=hable:desat=0,zscale=t=bt709:m=bt709:r=tv,format=yuv420p",
			[]string{"HDR tone mapping needs software decode and scale", "tone-mapped to SDR"}, []string{"washed out"}},
		{"vaapi uploads software frames", info(va, "scale_vaapi", "zscale", "tonemap"), hdr,
			"-init_hw_device vaapi=va:/dev/dri/renderD128 -filter_hw_device va",
			"scale=-2:1080,zscale=t=linear:npl=100,format=gbrpf32le,zscale=p=bt709,tonemap=tonemap=hable:desat=0,zscale=t=bt709:m=bt709:r=tv,format=nv12,hwupload",
			[]string{"tone mapping"}, nil},
		{"qsv uploads software frames", info(qsv, "vpp_qsv"),
			func(r Request) Request { r.Codec = "av1"; return r },
			"-init_hw_device qsv=hw -filter_hw_device hw", "scale=-2:1080,format=nv12,hwupload=extra_hw_frames=64",
			[]string{"av1 is not decoded on the GPU"}, nil},
		{"unknown source size scales to at most the limit", info(x264),
			func(r Request) Request { r.Width, r.Height = 0, 0; return r }, "",
			"scale=-2:'min(ih,1080)',format=yuv420p", nil, nil},
		{"no scale when the source fits", info(x264),
			func(r Request) Request { r.Height, r.Width, r.BitDepth = 720, 1280, 8; return r }, "",
			"format=yuv420p", nil, []string{"scaled from", "converted to 8-bit"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := uhd10()
			if tt.req != nil {
				r = tt.req(r)
			}
			s := tt.info.Plan(r)
			if s.GPU || strings.Join(s.Pre, " ") != tt.pre || s.Filter != tt.filter || s.Complex != "" {
				t.Errorf("GPU=%v\n pre    %q\n filter %q\n want   %q\n complex %q", s.GPU, strings.Join(s.Pre, " "), s.Filter, tt.filter, s.Complex)
			}
			for _, want := range tt.reasons {
				if !hasReason(s, want) {
					t.Errorf("reasons lack %q: %q", want, s.Reasons)
				}
			}
			for _, bad := range tt.absent {
				if hasReason(s, bad) {
					t.Errorf("reasons should not contain %q: %q", bad, s.Reasons)
				}
			}
		})
	}
}

func TestPlanHDRWithoutToneMapping(t *testing.T) {
	// No zscale/tonemap: plain conversion on the GPU, and the reasons warn.
	r := uhd10()
	r.HDR = true
	s := info(vt, "scale_vt").Plan(r)
	if !s.GPU || !hasReason(s, "washed out") || !hasReason(s, "no tone mapping") {
		t.Errorf("gpu=%v reasons=%q", s.GPU, s.Reasons)
	}
	// tonemap alone is not enough: it needs zscale to linearise first.
	s = info(x264, "tonemap").Plan(r)
	if !hasReason(s, "washed out") || strings.Contains(s.Filter, "tonemap") {
		t.Errorf("filter=%q reasons=%q", s.Filter, s.Reasons)
	}
	// An SDR 10-bit source is not HDR: no warning.
	r.HDR = false
	if s = info(vt, "scale_vt").Plan(r); hasReason(s, "washed out") || !hasReason(s, "10-bit") {
		t.Errorf("reasons=%q", s.Reasons)
	}
}

func TestPlanBurnIn(t *testing.T) {
	r := uhd10()
	r.BurnSubtitle = 3
	r.VideoIndex = 1

	// A GPU encoder still encodes, but decode, scale and overlay are software.
	s := info(vt, "scale_vt").Plan(r)
	want := "[0:1]scale=-2:1080[m];[0:3][m]scale2ref=w=main_w:h=main_h[sub][ref];[ref][sub]overlay=eof_action=pass,format=yuv420p[v]"
	if s.GPU || s.Filter != "" || s.Complex != want || len(s.Pre) != 0 {
		t.Errorf("vt: gpu=%v filter=%q complex=%q pre=%v", s.GPU, s.Filter, s.Complex, s.Pre)
	}
	if !hasReason(s, "subtitle burn-in needs software decode, scale and overlay") {
		t.Errorf("reasons = %q", s.Reasons)
	}

	// No scaling needed: the overlay goes straight onto the video.
	r.MaxHeight = 0
	s = info(x264).Plan(r)
	want = "[0:3][0:1]scale2ref=w=main_w:h=main_h[sub][ref];[ref][sub]overlay=eof_action=pass,format=yuv420p[v]"
	if s.Complex != want {
		t.Errorf("x264: complex=%q", s.Complex)
	}

	// VAAPI uploads the overlaid frames; HDR tone mapping runs after the overlay.
	r.HDR = true
	s = info(va, "zscale", "tonemap").Plan(r)
	if !strings.Contains(s.Complex, "overlay=eof_action=pass,zscale=t=linear:npl=100") ||
		!strings.HasSuffix(s.Complex, "format=nv12,hwupload[v]") {
		t.Errorf("va: complex=%q", s.Complex)
	}
}

func TestEncodeArgs(t *testing.T) {
	for _, enc := range []Encoder{vt, va, qsv, nv, x264} {
		a := encodeArgs(enc, 8_000_000)
		j := strings.Join(a, " ")
		if a[0] != "-c:v" || a[1] != enc.Name {
			t.Errorf("%s: %s", enc.Name, j)
		}
		for _, must := range []string{"-profile:v high", "-b:v 8000000", "-maxrate 8000000", "-g 100000"} {
			if enc.Kind == Software && must == "-b:v 8000000" {
				continue // libx264 is CRF with a cap
			}
			if !strings.Contains(j, must) {
				t.Errorf("%s lacks %q: %s", enc.Name, must, j)
			}
		}
	}
	// libx264 must never add keyframes of its own, and forced ones are IDR.
	j := strings.Join(encodeArgs(x264, 4_000_000), " ")
	for _, must := range []string{"-sc_threshold 0", "-forced-idr 1", "-crf 21", "-bufsize 8000000", "-preset veryfast"} {
		if !strings.Contains(j, must) {
			t.Errorf("libx264 lacks %q: %s", must, j)
		}
	}
}

func TestLadder(t *testing.T) {
	for h, want := range map[int]int{0: 8_000_000, 240: 1_000_000, 480: 2_000_000, 576: 4_000_000, 720: 4_000_000,
		721: 8_000_000, 1080: 8_000_000, 1081: 12_000_000, 1440: 12_000_000, 2160: 20_000_000} {
		if got := Ladder(h); got != want {
			t.Errorf("Ladder(%d) = %d, want %d", h, got, want)
		}
	}
	// The cap follows the OUTPUT height.
	if s := info(x264).Plan(uhd10()); s.Bitrate != 8_000_000 || !hasReason(s, "capped at 8 Mbps") {
		t.Errorf("1080p: %d %q", s.Bitrate, s.Reasons)
	}
	r := uhd10()
	r.MaxHeight = 720
	if s := info(x264).Plan(r); s.Bitrate != 4_000_000 || s.Height != 720 {
		t.Errorf("720p: %d, height %d", s.Bitrate, s.Height)
	}
}

func TestTarget(t *testing.T) {
	tests := []struct {
		r      Request
		w, h   int
		resize bool
	}{
		{Request{Width: 3840, Height: 2160, MaxHeight: 1080}, 1920, 1080, true},
		{Request{Width: 1920, Height: 1080, MaxHeight: 1080}, 1920, 1080, false},
		{Request{Width: 1280, Height: 720, MaxHeight: 0}, 1280, 720, false},
		{Request{Width: 720, Height: 576, MaxHeight: 480}, 600, 480, true},
		{Request{Width: 0, Height: 0, MaxHeight: 720}, -2, 720, true},
		{Request{Width: 0, Height: 0, MaxHeight: 0}, 0, 0, false},
	}
	for _, tt := range tests {
		if w, h, resize := target(tt.r); w != tt.w || h != tt.h || resize != tt.resize {
			t.Errorf("target(%+v) = %d,%d,%v want %d,%d,%v", tt.r, w, h, resize, tt.w, tt.h, tt.resize)
		}
	}
}

func TestSoftwareOnlyKeepsFilters(t *testing.T) {
	i := info(vt, "zscale", "tonemap")
	sw := i.SoftwareOnly()
	if sw.Encoder.Hardware() || !sw.Filters["zscale"] || !i.Encoder.Hardware() {
		t.Errorf("SoftwareOnly = %+v", sw.Encoder)
	}
	if !slices.Contains(sw.Plan(uhd10()).Encode, "libx264") {
		t.Error("software plan should encode with libx264")
	}
}
