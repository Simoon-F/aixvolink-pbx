package dtmf_test

import (
	"testing"

	"github.com/Simoon-F/aixvolink-pbx/internal/media/dtmf"
)

func TestEventRoundTrip(t *testing.T) {
	want := dtmf.Event{Code: 11, End: true, Volume: 10, Duration: 800}
	payload, err := dtmf.Encode(want)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	got, err := dtmf.Decode(payload)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got != want {
		t.Fatalf("Decode() = %+v, want %+v", got, want)
	}
}

func TestEventRejectsMalformedPayload(t *testing.T) {
	if _, err := dtmf.Decode([]byte{1, 2, 3}); err == nil {
		t.Fatal("Decode(short) error = nil")
	}
	if _, err := dtmf.Decode([]byte{17, 0, 0, 1}); err == nil {
		t.Fatal("Decode(code) error = nil")
	}
}
