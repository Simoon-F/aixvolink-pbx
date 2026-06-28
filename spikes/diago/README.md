# Diago G.711 RTP echo spike

This isolated program proves Diago can allocate an RTP/RTCP pair, parse PCMU (payload type 0) and PCMA (payload type 8), and echo RTP to the learned UDP source. It performs no SIP negotiation, transcoding, recording, or production port allocation.

## Run

```sh
go run ./spikes/diago/cmd -bind-ip 127.0.0.1 -port 16000 -idle-timeout 30s
```

Send 20 ms G.711 RTP packets to `127.0.0.1:16000`; echoed packets return to the sender. RTCP is bound at port 16001. The process exits on Ctrl-C or after the bounded idle timeout.

## Automated verification

```sh
go test -count=1 ./spikes/diago/...
go test -race -count=1 ./spikes/diago/...
go test -run='^$' -bench=. -benchmem ./spikes/diago/...
```

The test uses real loopback UDP sockets, verifies both G.711 payload types byte-for-byte, then checks cancellation and resource cleanup. Every network operation has a deadline.

## Phase 0 conclusion

Diago's media session is suitable as a reference and selective source of codec, SDP, RTP/RTCP, DTMF, and bridge behavior. The production media hot path remains behind AixvoLinkPBX-owned package boundaries so its allocation, queueing, source-validation, observability, and lifecycle policies can be benchmarked independently. This boundary is formalized in ADR-0003.
