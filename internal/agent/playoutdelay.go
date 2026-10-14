package agent

import (
	"strings"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

// playoutDelayURI is the RTP header extension that tells the browser how
// much playout delay it may add. Sending min=max=0 switches Chrome to its
// low-latency rendering path: every frame is shown as soon as it is
// decoded instead of being smoothed through the jitter buffer – the same
// technique cloud gaming services use.
const playoutDelayURI = "http://www.webrtc.org/experiments/rtp-hdrext/playout-delay"

type playoutDelayFactory struct{}

func (playoutDelayFactory) NewInterceptor(string) (interceptor.Interceptor, error) {
	return &playoutDelay{}, nil
}

type playoutDelay struct {
	interceptor.NoOp
}

func (p *playoutDelay) BindLocalStream(info *interceptor.StreamInfo, writer interceptor.RTPWriter) interceptor.RTPWriter {
	if !strings.HasPrefix(strings.ToLower(info.MimeType), "video/") {
		return writer
	}
	var id uint8
	for _, e := range info.RTPHeaderExtensions {
		if e.URI == playoutDelayURI {
			id = uint8(e.ID)
		}
	}
	if id == 0 {
		return writer // not negotiated by the viewer
	}
	ext, err := rtp.PlayoutDelayExtension{MinDelay: 0, MaxDelay: 0}.Marshal()
	if err != nil {
		return writer
	}
	return interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, a interceptor.Attributes) (int, error) {
		if err := h.SetExtension(id, ext); err != nil {
			return 0, err
		}
		return writer.Write(h, payload, a)
	})
}
