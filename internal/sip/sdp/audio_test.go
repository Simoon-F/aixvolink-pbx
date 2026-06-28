package sdp_test

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/Simoon-F/aixvolink-pbx/internal/sip/sdp"
)

func TestAudioOfferAnswerNegotiatesG711AndDTMF(t *testing.T) {
	offerBody := []byte("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=test\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 30000 RTP/AVP 8 0 101\r\na=rtpmap:8 PCMA/8000\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-16\r\na=rtcp:30001\r\na=sendrecv\r\n")
	answerBody := []byte("v=0\r\no=- 2 2 IN IP4 127.0.0.1\r\ns=test\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 31000 RTP/AVP 0 110\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:110 telephone-event/8000\r\na=rtcp:31001\r\n")
	offer, err := sdp.ParseAudio(offerBody)
	if err != nil {
		t.Fatalf("ParseAudio(offer) error = %v", err)
	}
	answer, err := sdp.ParseAudio(answerBody)
	if err != nil {
		t.Fatalf("ParseAudio(answer) error = %v", err)
	}
	mapping, err := sdp.Negotiate(offer, answer)
	if err != nil {
		t.Fatalf("Negotiate() error = %v", err)
	}
	if mapping.Name != "PCMU" || mapping.CallerPT != 0 || mapping.CalleePT != 0 || !mapping.HasDTMF || mapping.CallerDTMFPT != 101 || mapping.CalleeDTMFPT != 110 {
		t.Fatalf("mapping = %+v", mapping)
	}
}

func TestBuildAudioRoundTrips(t *testing.T) {
	body, err := sdp.BuildAudio(
		netip.MustParseAddrPort("127.0.0.1:32000"), netip.MustParseAddrPort("127.0.0.1:32001"),
		[]sdp.Codec{{Name: "PCMA", PayloadType: 8, ClockRate: 8000}, {Name: "TELEPHONE-EVENT", PayloadType: 101, ClockRate: 8000}},
		sdp.DirectionSendOnly,
	)
	if err != nil {
		t.Fatalf("BuildAudio() error = %v", err)
	}
	parsed, err := sdp.ParseAudio(body)
	if err != nil {
		t.Fatalf("ParseAudio() error = %v", err)
	}
	if parsed.RTP.String() != "127.0.0.1:32000" || parsed.RTCP.String() != "127.0.0.1:32001" || parsed.Direction != sdp.DirectionSendOnly {
		t.Fatalf("parsed endpoint = %+v", parsed)
	}
}

func TestNegotiateRejectsNoCommonCodec(t *testing.T) {
	offer := sdp.Endpoint{Codecs: []sdp.Codec{{Name: "PCMU", PayloadType: 0, ClockRate: 8000}}}
	answer := sdp.Endpoint{Codecs: []sdp.Codec{{Name: "PCMA", PayloadType: 8, ClockRate: 8000}}}
	if _, err := sdp.Negotiate(offer, answer); !errors.Is(err, sdp.ErrNoCommonCodec) {
		t.Fatalf("Negotiate() error = %v", err)
	}
}
