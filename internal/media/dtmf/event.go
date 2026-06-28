// Package dtmf implements the RFC 4733 telephone-event payload boundary.
package dtmf

import (
	"encoding/binary"
	"fmt"
)

const payloadSize = 4

// Event is one decoded telephone-event payload.
type Event struct {
	Code     uint8
	End      bool
	Volume   uint8
	Duration uint16
}

// Decode validates and decodes the fixed RFC 4733 payload.
func Decode(payload []byte) (Event, error) {
	if len(payload) < payloadSize {
		return Event{}, fmt.Errorf("telephone-event payload requires at least %d bytes", payloadSize)
	}
	event := Event{
		Code: payload[0], End: payload[1]&0x80 != 0,
		Volume: payload[1] & 0x3f, Duration: binary.BigEndian.Uint16(payload[2:4]),
	}
	if event.Code > 16 {
		return Event{}, fmt.Errorf("unsupported telephone-event code %d", event.Code)
	}
	return event, nil
}

// Encode returns the fixed RFC 4733 payload for a validated event.
func Encode(event Event) ([]byte, error) {
	if event.Code > 16 || event.Volume > 63 {
		return nil, fmt.Errorf("telephone-event code or volume is invalid")
	}
	payload := make([]byte, payloadSize)
	payload[0] = event.Code
	payload[1] = event.Volume
	if event.End {
		payload[1] |= 0x80
	}
	binary.BigEndian.PutUint16(payload[2:], event.Duration)
	return payload, nil
}
