package pionspike

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

const connectionTimeout = 10 * time.Second

func TestOfferEstablishesAudioAndEchoesRTP(t *testing.T) {
	service, err := NewService(Config{MaxPeers: 2, SessionTimeout: connectionTimeout})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	server := httptest.NewServer(service)
	t.Cleanup(func() {
		server.Close()
		if err := service.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	client, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("create client PeerConnection: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	localTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"microphone",
		"phase0-test",
	)
	if err != nil {
		t.Fatalf("create local audio track: %v", err)
	}
	if _, err := client.AddTrack(localTrack); err != nil {
		t.Fatalf("add local audio track: %v", err)
	}

	connected := make(chan struct{}, 1)
	received := make(chan *rtp.Packet, 1)
	client.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			select {
			case connected <- struct{}{}:
			default:
			}
		}
	})
	client.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		packet, _, readErr := track.ReadRTP()
		if readErr == nil {
			received <- packet
		}
	})

	offer, err := client.CreateOffer(nil)
	if err != nil {
		t.Fatalf("create offer: %v", err)
	}
	gatheringComplete := webrtc.GatheringCompletePromise(client)
	if err := client.SetLocalDescription(offer); err != nil {
		t.Fatalf("set local description: %v", err)
	}
	select {
	case <-gatheringComplete:
	case <-time.After(connectionTimeout):
		t.Fatal("client ICE gathering timed out")
	}

	offerBody, err := json.Marshal(client.LocalDescription())
	if err != nil {
		t.Fatalf("marshal offer: %v", err)
	}
	requestCtx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, server.URL+"/offer", bytes.NewReader(offerBody))
	if err != nil {
		t.Fatalf("create offer request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("exchange offer: %v", err)
	}
	t.Cleanup(func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close offer response: %v", err)
		}
	})
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("offer status = %d, body = %q", response.StatusCode, body)
	}
	var answer webrtc.SessionDescription
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&answer); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	if err := client.SetRemoteDescription(answer); err != nil {
		t.Fatalf("set remote description: %v", err)
	}

	select {
	case <-connected:
	case <-time.After(connectionTimeout):
		t.Fatal("PeerConnection did not connect")
	}

	wantPayload := []byte{0xf8, 0xff, 0xfe}
	for sequence := uint16(1); sequence <= 5; sequence++ {
		packet := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    111,
				SequenceNumber: sequence,
				Timestamp:      uint32(sequence) * 960,
				SSRC:           77,
			},
			Payload: wantPayload,
		}
		if err := localTrack.WriteRTP(packet); err != nil {
			t.Fatalf("write audio RTP: %v", err)
		}
	}

	select {
	case packet := <-received:
		if !bytes.Equal(packet.Payload, wantPayload) {
			t.Fatalf("echo payload = %v, want %v", packet.Payload, wantPayload)
		}
	case <-time.After(connectionTimeout):
		t.Fatal("audio RTP echo timed out")
	}
}
