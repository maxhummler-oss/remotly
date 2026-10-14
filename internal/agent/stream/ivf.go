package stream

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

type frameReader interface {
	readHeader() error
	Next() (Frame, error)
}

// ivfReader reads VP8 frames from an IVF stream (32-byte file header, then
// a 12-byte header with the size before every frame).
type ivfReader struct {
	r *bufio.Reader
}

func newIVFReader(r io.Reader) *ivfReader {
	return &ivfReader{r: bufio.NewReaderSize(r, 1<<20)}
}

func (v *ivfReader) readHeader() error {
	var hdr [32]byte
	if _, err := io.ReadFull(v.r, hdr[:]); err != nil {
		return err
	}
	if string(hdr[:4]) != "DKIF" {
		return errors.New("not an IVF stream")
	}
	if extra := int(binary.LittleEndian.Uint16(hdr[6:8])) - 32; extra > 0 {
		_, err := v.r.Discard(extra)
		return err
	}
	return nil
}

func (v *ivfReader) Next() (Frame, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(v.r, hdr[:]); err != nil {
		return Frame{}, err
	}
	size := binary.LittleEndian.Uint32(hdr[:4])
	if size > 16<<20 {
		return Frame{}, errors.New("ivf: oversized frame")
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(v.r, data); err != nil {
		return Frame{}, err
	}
	// VP8 frame tag: bit 0 clear = key frame.
	return Frame{Data: data, Keyframe: len(data) > 0 && data[0]&1 == 0}, nil
}
