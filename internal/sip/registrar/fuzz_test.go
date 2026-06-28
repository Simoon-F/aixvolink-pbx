package registrar

import (
	"testing"

	"github.com/emiago/sipgo/sip"
)

func FuzzSIPRegisterParser(f *testing.F) {
	seeds := []string{
		"REGISTER sip:pbx.example.invalid SIP/2.0\r\nVia: SIP/2.0/UDP 127.0.0.1:5062;branch=z9hG4bK-seed\r\nFrom: <sip:1001@pbx.example.invalid>;tag=seed\r\nTo: <sip:1001@pbx.example.invalid>\r\nCall-ID: seed@localhost\r\nCSeq: 1 REGISTER\r\nContact: <sip:1001@127.0.0.1:5062>;expires=60\r\nContent-Length: 0\r\n\r\n",
		"REGISTER sip:pbx.example.invalid SIP/2.0\r\nContent-Length: 999999999\r\n\r\n",
		"not sip",
		"",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, wire string) {
		parser := sip.NewParser()
		message, err := parser.ParseSIP([]byte(wire))
		if err != nil || message == nil {
			return
		}
		request, ok := message.(*sip.Request)
		if !ok {
			return
		}
		_ = request.To()
		_ = request.From()
		_ = request.Contact()
		_ = request.Via()
	})
}
