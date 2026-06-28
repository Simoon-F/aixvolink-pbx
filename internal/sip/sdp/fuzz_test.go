package sdp_test

import (
	"testing"

	"github.com/Simoon-F/aixvolink-pbx/internal/sip/sdp"
)

func FuzzParseAudio(f *testing.F) {
	f.Add("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=x\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 30000 RTP/AVP 0\r\n")
	f.Add("")
	f.Add("m=audio 999999 RTP/AVP 0\r\n")
	f.Fuzz(func(t *testing.T, body string) {
		_, _ = sdp.ParseAudio([]byte(body))
	})
}
