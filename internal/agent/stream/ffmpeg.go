// Package stream captures the screen (and audio) and encodes it using
// ffmpeg, which gives us hardware encoders (NVENC, Quick Sync, AMF,
// VideoToolbox) on every platform without linking any native code.
package stream

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Options controls capture and encoding.
type Options struct {
	FFmpeg  string // path to the ffmpeg binary
	Encoder string // "auto" or an ffmpeg encoder name
	Capture string // "auto", "ddagrab", "gdigrab", "avfoundation", "x11grab"
	Display int    // monitor index (ddagrab output / avfoundation screen)

	FPS              int
	BitrateKbps      int
	MaxWidth         int     // downscale wider screens; 0 = native
	KeyframeInterval float64 // seconds
	DrawCursor       bool

	// Advanced overrides.
	InputArgs   []string // replaces the generated capture input arguments
	EncoderArgs []string // appended after the generated encoder arguments

	Audio     string   // "auto", "off", "custom"
	AudioArgs []string // ffmpeg input args for custom audio

	// Launch starts ffmpeg. Defaults to a plain child process; the Windows
	// service worker substitutes one that starts it on the current input
	// desktop (needed for the lock and logon screens).
	Launch Launcher
}

// CaptureInfo describes which area of the desktop the stream shows, so the
// input injector can map normalized coordinates correctly.
type CaptureInfo struct {
	Method string
	// VirtualDesktop is true when the stream covers all monitors (gdigrab),
	// false when it shows a single display.
	VirtualDesktop bool
	Display        int
}

// Variant is one way of capturing the screen. Several are tried in order of
// performance until one works on this machine.
type Variant struct {
	Name     string
	Capture  CaptureInfo
	Input    []string
	Filter   string // -vf; empty = none
	ZeroCopy bool   // frames stay on the GPU from capture to encoder
}

// Codec is a video codec the agent can stream.
type Codec string

const (
	// H264 is preferred: hardware encoders everywhere, hardware decoding in
	// every browser.
	H264 Codec = "h264"
	// VP8 is the fallback for browsers built without H.264 (some Linux
	// Chromium builds). Software-encoded.
	VP8 Codec = "vp8"
)

// CodecOf returns the codec an ffmpeg encoder produces.
func CodecOf(encoder string) Codec {
	if strings.HasPrefix(encoder, "libvpx") || strings.HasPrefix(encoder, "vp8") {
		return VP8
	}
	return H264
}

// IsHardware reports whether an encoder runs on dedicated hardware.
func IsHardware(enc string) bool {
	switch enc {
	case "h264_nvenc", "h264_amf", "h264_qsv", "h264_videotoolbox":
		return true
	}
	return false
}

// encoderPreference lists encoders in order of preference per OS.
func encoderPreference(codec Codec) []string {
	if codec == VP8 {
		return []string{"libvpx"}
	}
	switch runtime.GOOS {
	case "windows":
		return []string{"h264_nvenc", "h264_amf", "h264_qsv", "libx264", "h264_mf"}
	case "darwin":
		return []string{"h264_videotoolbox", "libx264"}
	default:
		return []string{"h264_nvenc", "h264_qsv", "libx264"}
	}
}

// FFmpegPath resolves the ffmpeg binary: explicit setting, next to the agent
// executable, or $PATH.
func FFmpegPath(configured string) string {
	if configured != "" {
		return configured
	}
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name = "ffmpeg.exe"
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	// Common install locations that are often missing from a service's PATH.
	for _, p := range []string{"/opt/homebrew/bin/ffmpeg", "/usr/local/bin/ffmpeg", "/usr/bin/ffmpeg"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return name
}

func ffmpegOutput(ctx context.Context, ffmpeg string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// hasComponent reports whether ffmpeg lists name in the given section
// ("-filters", "-encoders").
func hasComponent(ffmpeg, section, name string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := ffmpegOutput(ctx, ffmpeg, "-hide_banner", section)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == name {
			return true
		}
	}
	return false
}

// testEncoder runs a tiny encode to verify the encoder actually works on this
// machine (a listed hardware encoder may lack a GPU or driver).
func testEncoder(ffmpeg, enc string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	args := []string{"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30", "-frames:v", "10"}
	args = append(args, encoderArgs(enc, Options{FPS: 30, BitrateKbps: 5000, KeyframeInterval: 2}, false)...)
	args = append(args, "-f", "null", "-")
	out, err := ffmpegOutput(ctx, ffmpeg, args...)
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(lastLines(out, 3)))
	}
	return nil
}

// SelectEncoder returns the first working encoder for codec. log receives
// progress messages for each candidate.
func SelectEncoder(opts Options, codec Codec, log func(format string, args ...any)) (string, error) {
	if _, err := exec.LookPath(opts.FFmpeg); err != nil {
		return "", fmt.Errorf("ffmpeg not found (%s): install ffmpeg or set \"ffmpeg\" in the agent config", opts.FFmpeg)
	}
	candidates := encoderPreference(codec)
	if codec == H264 && opts.Encoder != "" && opts.Encoder != "auto" {
		candidates = []string{opts.Encoder}
	}
	var lastErr error
	for _, enc := range candidates {
		if err := testEncoder(opts.FFmpeg, enc); err != nil {
			log("encoder %s unavailable: %v", enc, err)
			lastErr = err
			continue
		}
		log("using encoder %s", enc)
		return enc, nil
	}
	return "", fmt.Errorf("no working %s encoder: %v", codec, lastErr)
}

// Variants returns the capture variants to try, fastest first.
func Variants(opts Options, enc string) []Variant {
	fps := strconv.Itoa(opts.FPS)
	cursor := "0"
	if opts.DrawCursor {
		cursor = "1"
	}
	scale := `scale=w=trunc(iw/2)*2:h=trunc(ih/2)*2:flags=bicubic`
	if opts.MaxWidth > 0 {
		scale = fmt.Sprintf(`scale=w=min(iw\,%d):h=-2:flags=bicubic`, opts.MaxWidth)
	}

	if len(opts.InputArgs) > 0 {
		return []Variant{{Name: "custom", Capture: CaptureInfo{Method: "custom", Display: opts.Display}, Input: opts.InputArgs, Filter: scale}}
	}

	method := opts.Capture
	if method == "" {
		method = "auto"
	}
	var list []Variant
	switch runtime.GOOS {
	case "windows":
		dda := []string{"-f", "lavfi", "-i", fmt.Sprintf("ddagrab=output_idx=%d:framerate=%s:draw_mouse=%s:dup_frames=1",
			opts.Display, fps, map[string]string{"0": "false", "1": "true"}[cursor])}
		ddaInfo := CaptureInfo{Method: "ddagrab", Display: opts.Display}
		if (method == "auto" || method == "ddagrab") && hasComponent(opts.FFmpeg, "-filters", "ddagrab") {
			// Zero-copy: Desktop Duplication texture -> GPU encoder. No
			// CPU readback, no colour conversion on the CPU. Only possible
			// without scaling.
			if opts.MaxWidth == 0 {
				switch enc {
				case "h264_nvenc", "h264_amf":
					list = append(list, Variant{Name: "ddagrab-zerocopy", Capture: ddaInfo, Input: dda, ZeroCopy: true})
				case "h264_qsv":
					list = append(list, Variant{Name: "ddagrab-zerocopy", Capture: ddaInfo, Input: dda, ZeroCopy: true,
						Filter: "hwmap=derive_device=qsv,format=qsv"})
				}
			}
			list = append(list, Variant{Name: "ddagrab", Capture: ddaInfo, Input: dda, Filter: "hwdownload,format=bgra," + scale})
		}
		if method == "auto" || method == "gdigrab" || len(list) == 0 {
			list = append(list, Variant{
				Name:    "gdigrab",
				Capture: CaptureInfo{Method: "gdigrab", VirtualDesktop: true},
				Input:   []string{"-f", "gdigrab", "-framerate", fps, "-draw_mouse", cursor, "-i", "desktop"},
				Filter:  scale,
			})
		}
	case "darwin":
		list = append(list, Variant{
			Name:    "avfoundation",
			Capture: CaptureInfo{Method: "avfoundation", Display: opts.Display},
			Input: []string{"-f", "avfoundation", "-capture_cursor", cursor, "-framerate", fps,
				"-i", fmt.Sprintf("Capture screen %d:none", opts.Display)},
			Filter: scale,
		})
	default:
		display := os.Getenv("DISPLAY")
		if display == "" {
			display = ":0"
		}
		list = append(list, Variant{
			Name:    "x11grab",
			Capture: CaptureInfo{Method: "x11grab"},
			Input:   []string{"-f", "x11grab", "-framerate", fps, "-draw_mouse", cursor, "-i", display},
			Filter:  scale,
		})
	}
	return list
}

// encoderArgs returns low-latency settings for each encoder. All of them
// disable B-frames and lookahead, use CBR with a small VBV buffer and produce
// Constrained Baseline streams every browser decodes in hardware.
func encoderArgs(enc string, opts Options, zeroCopy bool) []string {
	br := opts.BitrateKbps
	gop := int(float64(opts.FPS) * opts.KeyframeInterval)
	if gop < 1 {
		gop = opts.FPS * 2
	}
	// About one frame of VBV buffer: every frame fits into one network
	// burst, so no frame waits behind a large predecessor.
	bufsize := br * 3 / 2 / max(opts.FPS, 1)
	args := []string{"-c:v", enc,
		"-b:v", fmt.Sprintf("%dk", br),
		"-maxrate", fmt.Sprintf("%dk", br),
		"-bufsize", fmt.Sprintf("%dk", max(bufsize, 300)),
		"-g", strconv.Itoa(gop),
		"-bf", "0",
	}
	switch enc {
	case "libx264":
		args = append(args, "-preset", "ultrafast", "-tune", "zerolatency",
			"-profile:v", "baseline", "-pix_fmt", "yuv420p")
	case "h264_nvenc":
		args = append(args, "-preset", "p1", "-tune", "ull", "-zerolatency", "1",
			"-rc", "cbr", "-delay", "0", "-rc-lookahead", "0", "-profile:v", "baseline")
	case "h264_qsv":
		args = append(args, "-preset", "veryfast", "-async_depth", "1", "-profile:v", "baseline")
		if !zeroCopy {
			args = append(args, "-pix_fmt", "nv12")
		}
	case "h264_amf":
		args = append(args, "-usage", "ultralowlatency", "-quality", "speed",
			"-rc", "cbr", "-profile:v", "constrained_baseline")
	case "h264_videotoolbox":
		args = append(args, "-realtime", "1", "-profile:v", "baseline", "-pix_fmt", "nv12")
	case "h264_mf":
		args = append(args, "-rate_control", "cbr", "-scenario", "display_remoting",
			"-hw_encoding", "1", "-pix_fmt", "nv12")
	case "libvpx":
		args = append(args, "-deadline", "realtime", "-cpu-used", "8", "-lag-in-frames", "0",
			"-auto-alt-ref", "0", "-error-resilient", "1", "-static-thresh", "0",
			"-threads", strconv.Itoa(min(runtime.NumCPU(), 16)), "-pix_fmt", "yuv420p")
	}
	return append(args, opts.EncoderArgs...)
}

// BuildArgs returns the full ffmpeg command line for live streaming.
func BuildArgs(enc string, v Variant, opts Options) []string {
	args := []string{"-hide_banner", "-loglevel", "warning", "-nostdin",
		"-fflags", "nobuffer", "-probesize", "32", "-analyzeduration", "0"}
	args = append(args, v.Input...)
	args = append(args, "-an")
	if v.Filter != "" {
		args = append(args, "-vf", v.Filter)
	}
	args = append(args, "-fps_mode", "passthrough")
	args = append(args, encoderArgs(enc, opts, v.ZeroCopy)...)
	// FLV (H.264) and IVF (VP8) carry explicit frame sizes, so every frame
	// is forwarded the moment it is complete. Flush every packet.
	if CodecOf(enc) == VP8 {
		return append(args, "-flush_packets", "1", "-f", "ivf", "pipe:1")
	}
	return append(args, "-flush_packets", "1", "-flvflags", "no_duration_filesize", "-f", "flv", "pipe:1")
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
