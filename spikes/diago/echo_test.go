package diagospike

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/emiago/diago/media"
	"github.com/pion/rtp"
)

const testTimeout = 3 * time.Second

func TestEchoReturnsPCMUAndPCMA(t *testing.T) {
	echo, err := NewEcho(Config{
		BindIP:      net.IPv4(127, 0, 0, 1),
		Port:        0,
		IdleTimeout: testTimeout,
	})
	if err != nil {
		t.Fatalf("NewEcho() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- echo.Run(ctx) }()

	select {
	case <-echo.Ready():
	case <-time.After(testTimeout):
		t.Fatal("echo did not become ready")
	}

	conn, err := net.DialUDP("udp4", nil, addressPointer(echo.LocalAddr()))
	if err != nil {
		t.Fatalf("dial RTP echo: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close RTP connection: %v", err)
		}
	})
	if err := conn.SetDeadline(time.Now().Add(testTimeout)); err != nil {
		t.Fatalf("set RTP deadline: %v", err)
	}

	for index, payloadType := range []uint8{media.CodecAudioUlaw.PayloadType, media.CodecAudioAlaw.PayloadType} {
		payload := bytes.Repeat([]byte{byte(index + 1)}, 160)
		packet := rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    payloadType,
				SequenceNumber: uint16(index + 1),
				Timestamp:      uint32(index * 160),
				SSRC:           42,
			},
			Payload: payload,
		}
		encoded, err := packet.Marshal()
		if err != nil {
			t.Fatalf("marshal RTP: %v", err)
		}
		if _, err := conn.Write(encoded); err != nil {
			t.Fatalf("write RTP: %v", err)
		}

		responseBuffer := make([]byte, media.RTPBufSize)
		n, err := conn.Read(responseBuffer)
		if err != nil {
			t.Fatalf("read echoed RTP: %v", err)
		}
		var echoed rtp.Packet
		if err := echoed.Unmarshal(responseBuffer[:n]); err != nil {
			t.Fatalf("unmarshal echoed RTP: %v", err)
		}
		if echoed.PayloadType != payloadType {
			t.Fatalf("payload type = %d, want %d", echoed.PayloadType, payloadType)
		}
		if !bytes.Equal(echoed.Payload, payload) {
			t.Fatal("echoed payload differs from sent payload")
		}
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("echo did not stop after cancellation")
	}
}

func BenchmarkDiagoRTPParse(b *testing.B) {
	packet := rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: media.CodecAudioUlaw.PayloadType, SSRC: 42},
		Payload: make([]byte, 160),
	}
	encoded, err := packet.Marshal()
	if err != nil {
		b.Fatalf("marshal RTP: %v", err)
	}
	buffer := make([]byte, media.RTPBufSize)
	copy(buffer, encoded)
	parsed := rtp.Packet{}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := media.RTPUnmarshal(buffer[:len(encoded)], &parsed); err != nil {
			b.Fatalf("unmarshal RTP: %v", err)
		}
	}
}

func addressPointer(addr net.UDPAddr) *net.UDPAddr {
	return &addr
}
