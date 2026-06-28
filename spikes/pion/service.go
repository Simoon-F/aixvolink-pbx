// Package pionspike validates browser audio termination with Pion WebRTC.
package pionspike

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

const maxOfferBytes = 1 << 20

//go:embed web/index.html
var webFiles embed.FS

// Config bounds peer allocation and session lifetime.
type Config struct {
	MaxPeers       int
	SessionTimeout time.Duration
}

// Service handles browser signaling and owns all Pion peer connections.
type Service struct {
	cfg    Config
	mux    *http.ServeMux
	mu     sync.Mutex
	closed bool
	peers  map[*webrtc.PeerConnection]*peerState
	wg     sync.WaitGroup
}

type peerState struct {
	connection *webrtc.PeerConnection
	timer      *time.Timer
	closeOnce  sync.Once
}

// NewService constructs an HTTP signaling service without starting a listener.
func NewService(cfg Config) (*Service, error) {
	if cfg.MaxPeers <= 0 {
		return nil, fmt.Errorf("max peers must be positive")
	}
	if cfg.SessionTimeout < time.Second {
		return nil, fmt.Errorf("session timeout must be at least one second")
	}

	s := &Service{
		cfg:   cfg,
		mux:   http.NewServeMux(),
		peers: make(map[*webrtc.PeerConnection]*peerState, cfg.MaxPeers),
	}
	s.mux.HandleFunc("GET /", s.serveIndex)
	s.mux.HandleFunc("POST /offer", s.handleOffer)
	return s, nil
}

// ServeHTTP implements http.Handler.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Close releases every active PeerConnection and waits for owned goroutines.
func (s *Service) Close() error {
	s.mu.Lock()
	s.closed = true
	states := make([]*peerState, 0, len(s.peers))
	for _, state := range s.peers {
		states = append(states, state)
	}
	s.mu.Unlock()

	var closeErr error
	for _, state := range states {
		closeErr = errors.Join(closeErr, s.closePeer(state))
	}
	s.wg.Wait()
	return closeErr
}

func (s *Service) serveIndex(w http.ResponseWriter, _ *http.Request) {
	page, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "embedded page unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(page)
}

func (s *Service) handleOffer(w http.ResponseWriter, r *http.Request) {
	var offer webrtc.SessionDescription
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxOfferBytes))
	if err := decoder.Decode(&offer); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_offer")
		return
	}
	if offer.Type != webrtc.SDPTypeOffer || strings.TrimSpace(offer.SDP) == "" {
		writeError(w, http.StatusBadRequest, "offer_required")
		return
	}

	state, err := s.newPeer()
	if err != nil {
		if errors.Is(err, errPeerLimit) || errors.Is(err, errServiceClosed) {
			writeError(w, http.StatusServiceUnavailable, "peer_capacity_unavailable")
			return
		}
		writeError(w, http.StatusInternalServerError, "peer_creation_failed")
		return
	}
	succeeded := false
	defer func() {
		if !succeeded {
			_ = s.closePeer(state)
		}
	}()

	if err := state.connection.SetRemoteDescription(offer); err != nil {
		writeError(w, http.StatusBadRequest, "remote_description_rejected")
		return
	}
	answer, err := state.connection.CreateAnswer(nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "answer_creation_failed")
		return
	}
	gatheringComplete := webrtc.GatheringCompletePromise(state.connection)
	if err := state.connection.SetLocalDescription(answer); err != nil {
		writeError(w, http.StatusInternalServerError, "local_description_failed")
		return
	}

	select {
	case <-gatheringComplete:
	case <-r.Context().Done():
		writeError(w, http.StatusRequestTimeout, "gathering_timeout")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(state.connection.LocalDescription()); err != nil {
		return
	}
	succeeded = true
}

var (
	errPeerLimit     = errors.New("peer limit reached")
	errServiceClosed = errors.New("service closed")
)

func (s *Service) newPeer() (*peerState, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errServiceClosed
	}
	if len(s.peers) >= s.cfg.MaxPeers {
		s.mu.Unlock()
		return nil, errPeerLimit
	}
	s.mu.Unlock()

	connection, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, fmt.Errorf("create PeerConnection: %w", err)
	}
	echoTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"audio",
		"aixvolinkpbx-phase0",
	)
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("create echo track: %w", err)
	}
	sender, err := connection.AddTrack(echoTrack)
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("add echo track: %w", err)
	}

	state := &peerState{connection: connection}
	state.timer = time.AfterFunc(s.cfg.SessionTimeout, func() { _ = s.closePeer(state) })

	s.mu.Lock()
	if s.closed || len(s.peers) >= s.cfg.MaxPeers {
		s.mu.Unlock()
		state.timer.Stop()
		_ = connection.Close()
		if s.closed {
			return nil, errServiceClosed
		}
		return nil, errPeerLimit
	}
	s.peers[connection] = state
	s.mu.Unlock()

	connection.OnConnectionStateChange(func(connectionState webrtc.PeerConnectionState) {
		switch connectionState {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			_ = s.closePeer(state)
		case webrtc.PeerConnectionStateUnknown, webrtc.PeerConnectionStateNew, webrtc.PeerConnectionStateConnecting,
			webrtc.PeerConnectionStateConnected, webrtc.PeerConnectionStateDisconnected:
		}
	})
	connection.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if remote.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		for {
			packet, _, readErr := remote.ReadRTP()
			if readErr != nil {
				return
			}
			if writeErr := echoTrack.WriteRTP(packet); writeErr != nil {
				return
			}
		}
	})

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		buffer := make([]byte, 1500)
		for {
			if _, _, readErr := sender.Read(buffer); readErr != nil {
				return
			}
		}
	}()

	return state, nil
}

func (s *Service) closePeer(state *peerState) error {
	var closeErr error
	state.closeOnce.Do(func() {
		state.timer.Stop()
		s.mu.Lock()
		delete(s.peers, state.connection)
		s.mu.Unlock()
		closeErr = state.connection.Close()
	})
	return closeErr
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Code string `json:"code"`
	}{Code: code})
}
