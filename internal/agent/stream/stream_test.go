package stream

import (
	"bytes"
	"encoding/binary"
	"os/exec"
	"testing"
	"time"
)

func TestOpusDuration(t *testing.T) {
	cases := []struct {
		toc  []byte
		want time.Duration
	}{
		{[]byte{0x00}, 10 * time.Millisecond},             // SILK 10 ms, 1 frame
		{[]byte{0x08}, 20 * time.Millisecond},             // SILK 20 ms
		{[]byte{(16 << 3) | 0}, 2500 * time.Microsecond},  // CELT 2.5 ms
		{[]byte{(18 << 3) | 0}, 10 * time.Millisecond},    // CELT 10 ms
		{[]byte{(18 << 3) | 1}, 20 * time.Millisecond},    // 2 frames
		{[]byte{(19 << 3) | 3, 3}, 60 * time.Millisecond}, // 3 x 20 ms
		{nil, 0},
	}
	for _, c := range cases {
		if got := opusDuration(c.toc); got != c.want {
			t.Errorf("opusDuration(%x) = %v, want %v", c.toc, got, c.want)
		}
	}
}

func oggPage(headerType byte, segments [][]byte) []byte {
	var lacing, body []byte
	for _, s := range segments {
		n := len(s)
		for n >= 255 {
			lacing = append(lacing, 255)
			n -= 255
		}
		lacing = append(lacing, byte(n))
		body = append(body, s...)
	}
	hdr := make([]byte, 27)
	copy(hdr, "OggS")
	hdr[5] = headerType
	hdr[26] = byte(len(lacing))
	return append(append(hdr, lacing...), body...)
}

func TestOggReaderSplitsPackets(t *testing.T) {
	big := bytes.Repeat([]byte{7}, 300) // spans two lacing values
	var stream []byte
	stream = append(stream, oggPage(2, [][]byte{[]byte("OpusHead1234")})...)
	stream = append(stream, oggPage(0, [][]byte{{1, 2, 3}, big})...)
	r := newOggReader(bytes.NewReader(stream))
	want := [][]byte{[]byte("OpusHead1234"), {1, 2, 3}, big}
	for i, w := range want {
		got, err := r.next()
		if err != nil {
			t.Fatalf("packet %d: %v", i, err)
		}
		if !bytes.Equal(got, w) {
			t.Fatalf("packet %d: got %d bytes, want %d", i, len(got), len(w))
		}
	}
}

func TestIVFReader(t *testing.T) {
	var b bytes.Buffer
	hdr := make([]byte, 32)
	copy(hdr, "DKIF")
	binary.LittleEndian.PutUint16(hdr[6:], 32)
	b.Write(hdr)
	for _, f := range [][]byte{{0x10, 1, 2}, {0x11, 3}} { // key frame, inter frame
		fh := make([]byte, 12)
		binary.LittleEndian.PutUint32(fh, uint32(len(f)))
		b.Write(fh)
		b.Write(f)
	}
	r := newIVFReader(&b)
	if err := r.readHeader(); err != nil {
		t.Fatal(err)
	}
	f1, _ := r.Next()
	f2, _ := r.Next()
	if !f1.Keyframe || f2.Keyframe || len(f1.Data) != 3 {
		t.Fatalf("unexpected frames: %+v %+v", f1, f2)
	}
}

func TestFLVReaderAddsParameterSets(t *testing.T) {
	sps := []byte{0x67, 0x42, 0xe0, 0x1f}
	pps := []byte{0x68, 0xce, 0x3c, 0x80}
	idr := []byte{0x65, 0x88, 0x84}
	cfg := []byte{1, 0x42, 0xe0, 0x1f, 0xff, 0xe1, 0, byte(len(sps))}
	cfg = append(cfg, sps...)
	cfg = append(cfg, 1, 0, byte(len(pps)))
	cfg = append(cfg, pps...)

	tag := func(body []byte) []byte {
		h := []byte{9, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body)), 0, 0, 0, 0, 0, 0, 0}
		out := append(h, body...)
		return append(out, 0, 0, 0, 0)
	}
	var b bytes.Buffer
	b.Write([]byte{'F', 'L', 'V', 1, 1, 0, 0, 0, 9, 0, 0, 0, 0})
	b.Write(tag(append([]byte{0x17, 0, 0, 0, 0}, cfg...)))
	nalu := append([]byte{0, 0, 0, byte(len(idr))}, idr...)
	b.Write(tag(append([]byte{0x17, 1, 0, 0, 0}, nalu...)))

	r := newFLVReader(&b)
	if err := r.readHeader(); err != nil {
		t.Fatal(err)
	}
	f, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	want := append(append(append(append(append([]byte{}, startCode...), sps...), startCode...), pps...), append(startCode, idr...)...)
	if !f.Keyframe || !bytes.Equal(f.Data, want) {
		t.Fatalf("got %x keyframe=%v, want %x", f.Data, f.Keyframe, want)
	}
}

// TestFFmpegPipeline runs real ffmpeg (if installed) through the FLV path.
func TestFFmpegPipeline(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	opts := Options{FFmpeg: "ffmpeg", Encoder: "libx264", FPS: 30, BitrateKbps: 4000, KeyframeInterval: 1,
		InputArgs: []string{"-re", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30"}}
	enc, err := SelectEncoder(opts, H264, t.Logf)
	if err != nil {
		t.Skip(err)
	}
	p := NewPipeline(opts, enc)
	sink := &countSink{ch: make(chan bool, 100)}
	p.Subscribe(sink)
	defer p.Unsubscribe(sink)
	timeout := time.After(10 * time.Second)
	for n := 0; n < 15; n++ {
		select {
		case key := <-sink.ch:
			if n == 0 && !key {
				t.Fatal("first delivered frame is not a keyframe")
			}
		case <-timeout:
			t.Fatalf("only %d frames received", n)
		}
	}
}

type countSink struct{ ch chan bool }

func (c *countSink) WriteFrame(data []byte, _ time.Duration) error {
	// Annex-B with IDR NAL (type 5) means keyframe.
	key := false
	for i := 0; i+4 < len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 && data[i+3]&0x1f == 5 {
			key = true
		}
	}
	select {
	case c.ch <- key:
	default:
	}
	return nil
}
