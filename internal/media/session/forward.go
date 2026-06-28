package session

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/Simoon-F/aixvolink-pbx/internal/media/dtmf"
	mediartp "github.com/Simoon-F/aixvolink-pbx/internal/media/rtp"
	"github.com/pion/rtp"
)

func (s *Session) forwardRTP(ctx context.Context, source, destination *endpoint, rewriter *mediartp.Rewriter) error {
	readConn, err := source.lease.RTPConn()
	if err != nil {
		return err
	}
	writeConn, err := destination.lease.RTPConn()
	if err != nil {
		return err
	}
	readBuffer := make([]byte, maxDatagramSize)
	writeBuffer := make([]byte, maxDatagramSize)
	for {
		if err := readConn.SetReadDeadline(time.Now().Add(s.cfg.ReadPollInterval)); err != nil {
			return fmt.Errorf("set RTP read deadline: %w", err)
		}
		readBytes, sourceAddress, err := readConn.ReadFromUDPAddrPort(readBuffer)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				s.updateTimeout(source, time.Now().UTC())
				continue
			}
			return fmt.Errorf("read %s RTP: %w", source.leg, err)
		}
		if !source.acceptsRTP(sourceAddress) {
			source.invalidPackets.Add(1)
			continue
		}
		var packet rtp.Packet
		if err := packet.Unmarshal(readBuffer[:readBytes]); err != nil || packet.Version != 2 || len(packet.Payload) == 0 {
			source.invalidPackets.Add(1)
			continue
		}
		remote := source.remote.Load()
		destinationRemote := destination.remote.Load()
		if remote == nil || destinationRemote == nil {
			continue
		}
		outputPayloadType, valid := payloadMapping(packet.PayloadType, remote, destinationRemote)
		if !valid {
			source.invalidPackets.Add(1)
			continue
		}
		class := source.tracker.Observe(&packet, time.Now().UTC(), readBytes)
		if class == mediartp.PacketDuplicate || !source.canSend() || !destination.canReceive() {
			continue
		}
		if remote.hasDTMF && packet.PayloadType == remote.dtmfPT {
			event, err := dtmf.Decode(packet.Payload)
			if err != nil {
				source.invalidPackets.Add(1)
				continue
			}
			if event.End {
				source.dtmfEvents.Add(1)
			}
		}
		output := rewriter.Rewrite(&packet, outputPayloadType)
		written, err := output.MarshalTo(writeBuffer)
		if err != nil {
			source.invalidPackets.Add(1)
			continue
		}
		target, exists := destination.rtpTarget()
		if !exists {
			continue
		}
		if _, err := writeConn.WriteToUDPAddrPort(writeBuffer[:written], target); err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("write %s RTP: %w", destination.leg, err)
		}
	}
}

func payloadMapping(input uint8, source, destination *remoteConfig) (uint8, bool) {
	if input == source.audioPT {
		return destination.audioPT, true
	}
	if source.hasDTMF && destination.hasDTMF && input == source.dtmfPT {
		return destination.dtmfPT, true
	}
	return 0, false
}

func (s *Session) updateTimeout(endpoint *endpoint, now time.Time) {
	stats := endpoint.tracker.Snapshot()
	reference := time.Unix(0, s.startedAt.Load())
	if !stats.LastPacketAt.IsZero() {
		reference = stats.LastPacketAt
	}
	endpoint.timedOut.Store(!reference.IsZero() && now.Sub(reference) >= s.cfg.InactivityTimeout)
}
