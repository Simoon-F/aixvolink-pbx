package session

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"time"

	mediartp "github.com/Simoon-F/aixvolink-pbx/internal/media/rtp"
	"github.com/pion/rtcp"
)

const ntpEpochOffset = 2208988800

func (s *Session) readRTCP(ctx context.Context, source *endpoint, outbound *mediartp.Rewriter) error {
	connection, err := source.lease.RTCPConn()
	if err != nil {
		return err
	}
	buffer := make([]byte, maxDatagramSize)
	for {
		if err := connection.SetReadDeadline(time.Now().Add(s.cfg.ReadPollInterval)); err != nil {
			return fmt.Errorf("set RTCP read deadline: %w", err)
		}
		readBytes, sourceAddress, err := connection.ReadFromUDPAddrPort(buffer)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			return fmt.Errorf("read %s RTCP: %w", source.leg, err)
		}
		if !source.acceptsRTCP(sourceAddress) {
			source.invalidPackets.Add(1)
			continue
		}
		packets, err := rtcp.Unmarshal(buffer[:readBytes])
		if err != nil {
			source.invalidPackets.Add(1)
			continue
		}
		now := time.Now().UTC()
		senderSSRC := outbound.Snapshot().SSRC
		for _, packet := range packets {
			switch report := packet.(type) {
			case *rtcp.SenderReport:
				source.lastRemoteSR.Store(compactNTP(report.NTPTime))
				source.lastRemoteSRNanos.Store(now.UnixNano())
				source.updateRemoteReport(report.Reports, senderSSRC, now)
			case *rtcp.ReceiverReport:
				source.updateRemoteReport(report.Reports, senderSSRC, now)
			}
		}
	}
}

func (s *Session) reportLoop(ctx context.Context) error {
	rtcpTicker := time.NewTicker(s.cfg.RTCPInterval)
	summaryTicker := time.NewTicker(s.cfg.SummaryInterval)
	defer rtcpTicker.Stop()
	defer summaryTicker.Stop()
	for {
		select {
		case now := <-rtcpTicker.C:
			if err := s.writeRTCP(s.caller, s.calleeToCaller, now.UTC()); err != nil {
				return err
			}
			if err := s.writeRTCP(s.callee, s.callerToCallee, now.UTC()); err != nil {
				return err
			}
		case <-summaryTicker.C:
			s.publishSummary(ctx)
		case <-ctx.Done():
			return nil
		}
	}
}

func (s *Session) writeRTCP(destination *endpoint, outbound *mediartp.Rewriter, now time.Time) error {
	target, exists := destination.rtcpTarget()
	if !exists {
		return nil
	}
	inbound := destination.tracker.Snapshot()
	sender := outbound.Snapshot()
	reports := make([]rtcp.ReceptionReport, 0, 1)
	if inbound.SSRC != 0 {
		reports = append(reports, receptionReport(destination, inbound, now))
	}
	var packet rtcp.Packet
	if sender.Packets > 0 {
		ntp := ntpTimestamp(now)
		destination.lastSentSR.Store(compactNTP(ntp))
		packet = &rtcp.SenderReport{
			SSRC: sender.SSRC, NTPTime: ntp, RTPTime: sender.LastTimestamp,
			PacketCount: uint32(min(sender.Packets, math.MaxUint32)), OctetCount: uint32(min(sender.Bytes, math.MaxUint32)),
			Reports: reports,
		}
	} else {
		packet = &rtcp.ReceiverReport{SSRC: sender.SSRC, Reports: reports}
	}
	wire, err := packet.Marshal()
	if err != nil {
		return fmt.Errorf("marshal %s RTCP report: %w", destination.leg, err)
	}
	connection, err := destination.lease.RTCPConn()
	if err != nil {
		return err
	}
	if _, err := connection.WriteToUDPAddrPort(wire, target); err != nil {
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		return fmt.Errorf("write %s RTCP report: %w", destination.leg, err)
	}
	return nil
}

func receptionReport(destination *endpoint, inbound mediartp.Stats, now time.Time) rtcp.ReceptionReport {
	expected := inbound.Packets + inbound.Lost
	fraction := uint8(0)
	if expected > 0 {
		fraction = uint8(min(inbound.Lost*256/expected, 255))
	}
	lastSR := destination.lastRemoteSR.Load()
	delay := uint32(0)
	if receivedNanos := destination.lastRemoteSRNanos.Load(); lastSR != 0 && receivedNanos != 0 {
		elapsed := now.Sub(time.Unix(0, receivedNanos))
		if elapsed > 0 {
			delay = uint32(min(uint64(elapsed*65536/time.Second), uint64(math.MaxUint32)))
		}
	}
	return rtcp.ReceptionReport{
		SSRC: inbound.SSRC, FractionLost: fraction, TotalLost: uint32(min(inbound.Lost, (1<<24)-1)),
		LastSequenceNumber: inbound.LastSequence, Jitter: uint32(max(0, inbound.JitterSamples)),
		LastSenderReport: lastSR, Delay: delay,
	}
}

func ntpTimestamp(value time.Time) uint64 {
	seconds := uint64(value.Unix() + ntpEpochOffset)
	fraction := uint64(value.Nanosecond()) * (1 << 32) / uint64(time.Second)
	return seconds<<32 | fraction
}

func compactNTP(value uint64) uint32 { return uint32(value >> 16) }
