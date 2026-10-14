package stream

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Frame is one H.264 access unit in Annex-B format.
type Frame struct {
	Data     []byte
	Keyframe bool
}

var startCode = []byte{0, 0, 0, 1}

// flvReader extracts H.264 access units from an FLV byte stream.
//
// ffmpeg writes FLV with one tag per encoded frame and explicit lengths, so
// every frame can be forwarded the instant it is complete. Raw Annex-B output
// would force us to wait for the next frame's start code to know where the
// current one ends, adding a full frame interval of latency.
type flvReader struct {
	r          *bufio.Reader
	lengthSize int
	sps, pps   [][]byte
	header     [11]byte
}

func newFLVReader(r io.Reader) *flvReader {
	return &flvReader{r: bufio.NewReaderSize(r, 1<<20), lengthSize: 4}
}

func (f *flvReader) readHeader() error {
	var hdr [9]byte
	if _, err := io.ReadFull(f.r, hdr[:]); err != nil {
		return err
	}
	if string(hdr[:3]) != "FLV" {
		return errors.New("not an FLV stream")
	}
	skip := int(binary.BigEndian.Uint32(hdr[5:9])) - 9 + 4 // rest of header + PreviousTagSize0
	_, err := f.r.Discard(skip)
	return err
}

// Next returns the next video frame, skipping non-video tags.
func (f *flvReader) Next() (Frame, error) {
	for {
		if _, err := io.ReadFull(f.r, f.header[:]); err != nil {
			return Frame{}, err
		}
		tagType := f.header[0] & 0x1f
		size := int(f.header[1])<<16 | int(f.header[2])<<8 | int(f.header[3])
		body := make([]byte, size)
		if _, err := io.ReadFull(f.r, body); err != nil {
			return Frame{}, err
		}
		if _, err := f.r.Discard(4); err != nil { // PreviousTagSize
			return Frame{}, err
		}
		if tagType != 9 || len(body) < 5 {
			continue
		}
		frameType := body[0] >> 4
		codecID := body[0] & 0x0f
		if codecID != 7 {
			return Frame{}, fmt.Errorf("unsupported FLV video codec %d (expected H.264)", codecID)
		}
		switch body[1] { // AVCPacketType
		case 0:
			if err := f.parseConfig(body[5:]); err != nil {
				return Frame{}, err
			}
		case 1:
			frame, err := f.toAnnexB(body[5:], frameType == 1)
			if err != nil {
				return Frame{}, err
			}
			if len(frame.Data) > 0 {
				return frame, nil
			}
		}
	}
}

// parseConfig reads an AVCDecoderConfigurationRecord.
func (f *flvReader) parseConfig(b []byte) error {
	if len(b) < 6 {
		return errors.New("short AVC config")
	}
	f.lengthSize = int(b[4]&3) + 1
	f.sps, f.pps = nil, nil
	n := int(b[5] & 0x1f)
	p := 6
	read := func() ([]byte, error) {
		if p+2 > len(b) {
			return nil, errors.New("truncated AVC config")
		}
		l := int(binary.BigEndian.Uint16(b[p:]))
		p += 2
		if p+l > len(b) {
			return nil, errors.New("truncated AVC config")
		}
		nal := append([]byte(nil), b[p:p+l]...)
		p += l
		return nal, nil
	}
	for i := 0; i < n; i++ {
		nal, err := read()
		if err != nil {
			return err
		}
		f.sps = append(f.sps, nal)
	}
	if p >= len(b) {
		return errors.New("truncated AVC config")
	}
	n = int(b[p])
	p++
	for i := 0; i < n; i++ {
		nal, err := read()
		if err != nil {
			return err
		}
		f.pps = append(f.pps, nal)
	}
	return nil
}

// toAnnexB converts length-prefixed NAL units to start-code format and makes
// sure every keyframe carries SPS/PPS so viewers can join at any keyframe.
func (f *flvReader) toAnnexB(b []byte, keyframe bool) (Frame, error) {
	out := make([]byte, 0, len(b)+64)
	hasParams := false
	var nals [][]byte
	for len(b) > 0 {
		if len(b) < f.lengthSize {
			return Frame{}, errors.New("truncated NAL length")
		}
		var l int
		for i := 0; i < f.lengthSize; i++ {
			l = l<<8 | int(b[i])
		}
		b = b[f.lengthSize:]
		if l > len(b) {
			return Frame{}, errors.New("truncated NAL unit")
		}
		nal := b[:l]
		b = b[l:]
		if len(nal) == 0 {
			continue
		}
		switch nal[0] & 0x1f {
		case 9: // access unit delimiter: useless for WebRTC
			continue
		case 7, 8:
			hasParams = true
		case 5:
			keyframe = true
		}
		nals = append(nals, nal)
	}
	if keyframe && !hasParams {
		for _, nal := range f.sps {
			out = append(append(out, startCode...), nal...)
		}
		for _, nal := range f.pps {
			out = append(append(out, startCode...), nal...)
		}
	}
	for _, nal := range nals {
		out = append(append(out, startCode...), nal...)
	}
	return Frame{Data: out, Keyframe: keyframe}, nil
}
