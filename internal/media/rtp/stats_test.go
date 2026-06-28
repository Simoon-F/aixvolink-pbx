package rtp_test

import (
	"testing"
	"time"

	mediartp "github.com/Simoon-F/aixvolink-pbx/internal/media/rtp"
	"github.com/pion/rtp"
)

func TestTrackerIdentifiesLossReorderDuplicateAndSSRCChange(t *testing.T) {
	tracker := mediartp.NewTracker(8000)
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	packets := []rtp.Packet{
		{Header: rtp.Header{Version: 2, SSRC: 10, SequenceNumber: 100, Timestamp: 1000}},
		{Header: rtp.Header{Version: 2, SSRC: 10, SequenceNumber: 102, Timestamp: 1320}},
		{Header: rtp.Header{Version: 2, SSRC: 10, SequenceNumber: 101, Timestamp: 1160}},
		{Header: rtp.Header{Version: 2, SSRC: 10, SequenceNumber: 101, Timestamp: 1160}},
		{Header: rtp.Header{Version: 2, SSRC: 11, SequenceNumber: 103, Timestamp: 1480}},
	}
	for index := range packets {
		tracker.Observe(&packets[index], now.Add(time.Duration(index)*20*time.Millisecond), 172)
	}
	stats := tracker.Snapshot()
	if stats.Packets != 5 || stats.Lost != 0 || stats.Reordered != 1 || stats.Duplicates != 1 || stats.SSRCChanges != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.JitterSamples < 0 || stats.LastSequence != 103 {
		t.Fatalf("jitter/sequence stats = %+v", stats)
	}
}

func TestRewriterCreatesIndependentStream(t *testing.T) {
	rewriter, err := mediartp.NewRewriter()
	if err != nil {
		t.Fatalf("NewRewriter() error = %v", err)
	}
	first := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 101, SSRC: 55, SequenceNumber: 42, Timestamp: 1000}, Payload: []byte{1, 2, 3, 4}}
	second := first
	second.SequenceNumber++
	second.Timestamp += 160
	outFirst := rewriter.Rewrite(&first, 110)
	outSecond := rewriter.Rewrite(&second, 110)
	if outFirst.SSRC == first.SSRC || outFirst.PayloadType != 110 || outSecond.SequenceNumber != outFirst.SequenceNumber+1 || outSecond.Timestamp != outFirst.Timestamp+160 {
		t.Fatalf("rewritten packets = %+v %+v", outFirst.Header, outSecond.Header)
	}
}

func BenchmarkRewrite(b *testing.B) {
	rewriter, err := mediartp.NewRewriter()
	if err != nil {
		b.Fatal(err)
	}
	packet := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SSRC: 55, SequenceNumber: 42, Timestamp: 1000}, Payload: make([]byte, 160)}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		packet.SequenceNumber++
		packet.Timestamp += 160
		_ = rewriter.Rewrite(&packet, 0)
	}
}

func BenchmarkTrackerObserve(b *testing.B) {
	tracker := mediartp.NewTracker(8000)
	packet := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SSRC: 55, SequenceNumber: 42, Timestamp: 1000}, Payload: make([]byte, 160)}
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	b.ReportAllocs()
	b.ResetTimer()
	for index := range b.N {
		packet.SequenceNumber++
		packet.Timestamp += 160
		tracker.Observe(&packet, now.Add(time.Duration(index)*20*time.Millisecond), 172)
	}
}
