package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"syscall"
	"time"
	"unsafe"
)

// WASAPI loopback capture of the default playback device, implemented with
// raw COM calls (no cgo). The PCM stream is piped into ffmpeg for Opus
// encoding.

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	clsidMMDeviceEnumerator = guid{0xBCDE0395, 0xE52F, 0x467C, [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator  = guid{0xA95664D2, 0x9614, 0x4F35, [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioClient         = guid{0x1CB9AD4C, 0xDBFA, 0x4C32, [8]byte{0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2}}
	iidIAudioCaptureClient  = guid{0xC8ADBD64, 0xE71E, 0x48A0, [8]byte{0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17}}
	subtypeFloat            = guid{0x00000003, 0x0000, 0x0010, [8]byte{0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}}
)

var (
	ole32               = syscall.NewLazyDLL("ole32.dll")
	procCoInitializeEx  = ole32.NewProc("CoInitializeEx")
	procCoUninitialize  = ole32.NewProc("CoUninitialize")
	procCoCreateInstace = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree   = ole32.NewProc("CoTaskMemFree")
)

const (
	clsctxAll             = 0x17
	eRender               = 0
	eConsole              = 0
	audclntShareShared    = 0
	audclntLoopback       = 0x00020000
	audclntBufferSilent   = 0x2
	waveFormatIEEEFloat   = 3
	waveFormatExtensible  = 0xFFFE
	rpcEChangedMode       = 0x80010106
	sFalse                = 1
	refTimesPerMillisec   = 10000
	captureBufferDuration = 50 * refTimesPerMillisec
)

type waveFormatEx struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	Size           uint16
}

type waveFormatExtensibleT struct {
	waveFormatEx
	Samples     uint16
	ChannelMask uint32
	SubFormat   guid
}

// comObj is a COM interface pointer (memory owned by COM, not Go).
type comObj unsafe.Pointer

func call(o comObj, method int, args ...uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(o)
	fn := *(*uintptr)(unsafe.Add(vtbl, uintptr(method)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(o)}, args...)...)
	return r
}

func release(o comObj) {
	if o != nil {
		call(o, 2)
	}
}

func hr(r uintptr, what string) error {
	if int32(r) < 0 {
		return fmt.Errorf("%s failed: 0x%08X", what, uint32(r))
	}
	return nil
}

type loopback struct {
	client  comObj
	capture comObj
	format  *waveFormatEx
	enum    comObj
	device  comObj
}

func (l *loopback) close() {
	if l.client != nil {
		call(l.client, 11) // Stop
	}
	release(l.capture)
	release(l.client)
	release(l.device)
	release(l.enum)
	if l.format != nil {
		procCoTaskMemFree.Call(uintptr(unsafe.Pointer(l.format)))
	}
}

// openLoopback must run on a thread with COM initialized.
func openLoopback() (*loopback, error) {
	l := &loopback{}
	r, _, _ := procCoCreateInstace.Call(uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&l.enum)))
	if err := hr(r, "CoCreateInstance(MMDeviceEnumerator)"); err != nil {
		return nil, err
	}
	// IMMDeviceEnumerator::GetDefaultAudioEndpoint
	if err := hr(call(l.enum, 4, eRender, eConsole, uintptr(unsafe.Pointer(&l.device))), "GetDefaultAudioEndpoint"); err != nil {
		l.close()
		return nil, errors.New("no default playback device")
	}
	// IMMDevice::Activate
	if err := hr(call(l.device, 3, uintptr(unsafe.Pointer(&iidIAudioClient)), clsctxAll, 0, uintptr(unsafe.Pointer(&l.client))), "Activate(IAudioClient)"); err != nil {
		l.close()
		return nil, err
	}
	// IAudioClient::GetMixFormat
	if err := hr(call(l.client, 8, uintptr(unsafe.Pointer(&l.format))), "GetMixFormat"); err != nil {
		l.close()
		return nil, err
	}
	// IAudioClient::Initialize
	if err := hr(call(l.client, 3, audclntShareShared, audclntLoopback, captureBufferDuration, 0,
		uintptr(unsafe.Pointer(l.format)), 0), "IAudioClient.Initialize"); err != nil {
		l.close()
		return nil, err
	}
	// IAudioClient::GetService(IAudioCaptureClient)
	if err := hr(call(l.client, 14, uintptr(unsafe.Pointer(&iidIAudioCaptureClient)), uintptr(unsafe.Pointer(&l.capture))), "GetService"); err != nil {
		l.close()
		return nil, err
	}
	return l, nil
}

// ffmpegFormat returns ffmpeg raw input args matching the mix format.
func (l *loopback) ffmpegFormat() ([]string, error) {
	f := l.format
	isFloat := f.FormatTag == waveFormatIEEEFloat
	if f.FormatTag == waveFormatExtensible {
		ext := (*waveFormatExtensibleT)(unsafe.Pointer(f))
		isFloat = ext.SubFormat == subtypeFloat
	}
	var sample string
	switch {
	case isFloat && f.BitsPerSample == 32:
		sample = "f32le"
	case !isFloat && f.BitsPerSample == 16:
		sample = "s16le"
	case !isFloat && f.BitsPerSample == 24:
		sample = "s24le"
	case !isFloat && f.BitsPerSample == 32:
		sample = "s32le"
	default:
		return nil, fmt.Errorf("unsupported mix format (%d bit, tag %#x)", f.BitsPerSample, f.FormatTag)
	}
	return []string{"-f", sample, "-ar", strconv.Itoa(int(f.SamplesPerSec)), "-ac", strconv.Itoa(int(f.Channels))}, nil
}

func comInit() (func(), error) {
	runtime.LockOSThread()
	r, _, _ := procCoInitializeEx.Call(0, 0) // COINIT_MULTITHREADED
	if int32(r) < 0 && uint32(r) != rpcEChangedMode {
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("CoInitializeEx: 0x%08X", uint32(r))
	}
	return func() {
		if int32(r) >= 0 {
			procCoUninitialize.Call()
		}
		runtime.UnlockOSThread()
	}, nil
}

// probeLoopback checks that loopback capture works and returns the ffmpeg
// input format arguments.
func probeLoopback() ([]string, error) {
	done, err := comInit()
	if err != nil {
		return nil, err
	}
	defer done()
	l, err := openLoopback()
	if err != nil {
		return nil, err
	}
	defer l.close()
	return l.ffmpegFormat()
}

// captureLoopback streams PCM from the default playback device to w until
// ctx is cancelled. When nothing is playing, WASAPI delivers no packets; we
// then write silence so the encoder keeps a steady clock.
func captureLoopback(ctx context.Context, w io.Writer) error {
	done, err := comInit()
	if err != nil {
		return err
	}
	defer done()
	l, err := openLoopback()
	if err != nil {
		return err
	}
	defer l.close()
	if err := hr(call(l.client, 10), "IAudioClient.Start"); err != nil {
		return err
	}
	block := int(l.format.BlockAlign)
	rate := int(l.format.SamplesPerSec)
	buf := make([]byte, 0, 64<<10)
	lastData := time.Now()
	var lastFill time.Time
	idle := false
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		got := false
		for {
			var packet uint32
			if err := hr(call(l.capture, 5, uintptr(unsafe.Pointer(&packet))), "GetNextPacketSize"); err != nil {
				return err
			}
			if packet == 0 {
				break
			}
			var data unsafe.Pointer
			var frames, flags uint32
			if err := hr(call(l.capture, 3, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&frames)),
				uintptr(unsafe.Pointer(&flags)), 0, 0), "GetBuffer"); err != nil {
				return err
			}
			n := int(frames) * block
			buf = buf[:0]
			if flags&audclntBufferSilent != 0 || data == nil {
				buf = append(buf, make([]byte, n)...)
			} else {
				buf = append(buf, unsafe.Slice((*byte)(data), n)...)
			}
			call(l.capture, 4, uintptr(frames)) // ReleaseBuffer
			if _, err := w.Write(buf); err != nil {
				return err
			}
			got = true
		}
		// When nothing plays, WASAPI delivers nothing. After a short idle
		// period, feed silence at the real-time rate so the encoder clock
		// keeps running; stop as soon as real data arrives.
		now := time.Now()
		switch {
		case got:
			lastData, idle = now, false
		case !idle && now.Sub(lastData) > 60*time.Millisecond:
			idle, lastFill = true, now
		case idle:
			frames := int(int64(rate) * int64(now.Sub(lastFill)) / int64(time.Second))
			if frames > 0 {
				if _, err := w.Write(make([]byte, frames*block)); err != nil {
					return err
				}
				lastFill = lastFill.Add(time.Duration(int64(frames) * int64(time.Second) / int64(rate)))
			}
		}
	}
}
