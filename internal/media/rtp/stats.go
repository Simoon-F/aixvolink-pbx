// Package rtp provides allocation-conscious RTP sequence and jitter accounting.
package rtp

import (
	"math"
	"sync/atomic"
	"time"

	pionrtp "github.com/pion/rtp"
)

const sequenceWindow = 128

// PacketClass classifies one sequence number.
type PacketClass uint8

const (
	PacketNew PacketClass = iota
	PacketReordered
	PacketDuplicate
)

// Stats is an immutable RTP quality snapshot.
type Stats struct {
	Packets       uint64
	Bytes         uint64
	Lost          uint64
	Duplicates    uint64
	Reordered     uint64
	SSRCChanges   uint64
	JitterSamples float64
	SSRC          uint32
	LastSequence  uint32
	LastPacketAt  time.Time
}

type publishedStats struct {
	packets         atomic.Uint64
	bytes           atomic.Uint64
	lost            atomic.Uint64
	duplicates      atomic.Uint64
	reordered       atomic.Uint64
	ssrcChanges     atomic.Uint64
	jitterBits      atomic.Uint64
	ssrc            atomic.Uint32
	lastSequence    atomic.Uint32
	lastPacketNanos atomic.Int64
}

// Tracker is owned by one RTP read loop and publishes lock-free snapshots.
type Tracker struct {
	clockRate uint32
	started   bool
	ssrc      uint32
	maxSeq    uint16
	cycles    uint32
	seen      [sequenceWindow]uint16
	seenValid [sequenceWindow]bool
	transit   int64
	jitter    float64
	published publishedStats
}

// NewTracker constructs a tracker for an RTP clock rate.
func NewTracker(clockRate uint32) *Tracker {
	return &Tracker{clockRate: clockRate}
}

// Observe accounts for one validated RTP packet and returns its sequence class.
func (t *Tracker) Observe(packet *pionrtp.Packet, receivedAt time.Time, wireBytes int) PacketClass {
	index := int(packet.SequenceNumber % sequenceWindow)
	if t.seenValid[index] && t.seen[index] == packet.SequenceNumber {
		t.published.duplicates.Add(1)
		t.publishArrival(packet, receivedAt, wireBytes)
		return PacketDuplicate
	}
	t.seenValid[index] = true
	t.seen[index] = packet.SequenceNumber

	class := PacketNew
	if !t.started {
		t.started = true
		t.maxSeq = packet.SequenceNumber
		t.ssrc = packet.SSRC
	} else {
		if packet.SSRC != t.ssrc {
			t.published.ssrcChanges.Add(1)
			t.ssrc = packet.SSRC
		}
		delta := int16(packet.SequenceNumber - t.maxSeq)
		switch {
		case delta > 0:
			if packet.SequenceNumber < t.maxSeq {
				t.cycles += 1 << 16
			}
			if delta > 1 {
				t.published.lost.Add(uint64(delta - 1))
			}
			t.maxSeq = packet.SequenceNumber
		case delta < 0:
			class = PacketReordered
			t.published.reordered.Add(1)
			decrementAtomic(&t.published.lost)
		}
	}

	arrivalSamples := receivedAt.UnixNano() * int64(t.clockRate) / int64(time.Second)
	transit := arrivalSamples - int64(packet.Timestamp)
	if t.published.packets.Load() > 0 {
		difference := transit - t.transit
		if difference < 0 {
			difference = -difference
		}
		t.jitter += (float64(difference) - t.jitter) / 16
	}
	t.transit = transit
	t.published.jitterBits.Store(math.Float64bits(t.jitter))
	t.publishArrival(packet, receivedAt, wireBytes)
	return class
}

func (t *Tracker) publishArrival(packet *pionrtp.Packet, receivedAt time.Time, wireBytes int) {
	t.published.packets.Add(1)
	if wireBytes > 0 {
		t.published.bytes.Add(uint64(wireBytes))
	}
	t.published.ssrc.Store(packet.SSRC)
	t.published.lastSequence.Store(t.cycles + uint32(packet.SequenceNumber))
	t.published.lastPacketNanos.Store(receivedAt.UnixNano())
}

func decrementAtomic(value *atomic.Uint64) {
	for {
		current := value.Load()
		if current == 0 || value.CompareAndSwap(current, current-1) {
			return
		}
	}
}

// Snapshot returns current counters without blocking the packet loop.
func (t *Tracker) Snapshot() Stats {
	nanos := t.published.lastPacketNanos.Load()
	var lastPacketAt time.Time
	if nanos != 0 {
		lastPacketAt = time.Unix(0, nanos).UTC()
	}
	return Stats{
		Packets: t.published.packets.Load(), Bytes: t.published.bytes.Load(), Lost: t.published.lost.Load(),
		Duplicates: t.published.duplicates.Load(), Reordered: t.published.reordered.Load(),
		SSRCChanges: t.published.ssrcChanges.Load(), JitterSamples: math.Float64frombits(t.published.jitterBits.Load()),
		SSRC: t.published.ssrc.Load(), LastSequence: t.published.lastSequence.Load(), LastPacketAt: lastPacketAt,
	}
}
