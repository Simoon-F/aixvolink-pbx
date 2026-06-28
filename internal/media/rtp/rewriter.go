package rtp

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync/atomic"

	pionrtp "github.com/pion/rtp"
)

// SenderStats is an immutable outbound stream snapshot.
type SenderStats struct {
	SSRC          uint32
	Packets       uint64
	Bytes         uint64
	LastTimestamp uint32
}

// Rewriter isolates the sequence, timestamp, SSRC, and payload mapping of one output leg.
type Rewriter struct {
	ssrc          uint32
	sequence      uint16
	timestamp     uint32
	inputBase     uint32
	outputBase    uint32
	initialized   bool
	packets       atomic.Uint64
	bytes         atomic.Uint64
	lastTimestamp atomic.Uint32
}

// NewRewriter constructs an output stream with cryptographically random identifiers.
func NewRewriter() (*Rewriter, error) {
	var seed [10]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return nil, fmt.Errorf("generate RTP stream identifiers: %w", err)
	}
	return &Rewriter{
		ssrc: binary.BigEndian.Uint32(seed[0:4]), sequence: binary.BigEndian.Uint16(seed[4:6]),
		timestamp: binary.BigEndian.Uint32(seed[6:10]), outputBase: binary.BigEndian.Uint32(seed[6:10]),
	}, nil
}

// Rewrite returns a detached header with the original payload and output mapping.
func (r *Rewriter) Rewrite(input *pionrtp.Packet, outputPayloadType uint8) pionrtp.Packet {
	if !r.initialized {
		r.initialized = true
		r.inputBase = input.Timestamp
	} else {
		r.sequence++
	}
	r.timestamp = r.outputBase + input.Timestamp - r.inputBase
	output := *input
	output.Header = input.Header
	output.PayloadType = outputPayloadType
	output.SSRC = r.ssrc
	output.SequenceNumber = r.sequence
	output.Timestamp = r.timestamp
	r.packets.Add(1)
	r.bytes.Add(uint64(len(input.Payload)))
	r.lastTimestamp.Store(r.timestamp)
	return output
}

// Snapshot returns output counters for RTCP sender reports.
func (r *Rewriter) Snapshot() SenderStats {
	return SenderStats{SSRC: r.ssrc, Packets: r.packets.Load(), Bytes: r.bytes.Load(), LastTimestamp: r.lastTimestamp.Load()}
}
