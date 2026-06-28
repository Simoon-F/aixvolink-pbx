# Pion browser WebRTC audio spike

This isolated program serves a browser page that captures microphone audio, exchanges an SDP offer/answer with Pion, establishes ICE plus DTLS-SRTP, and plays the echoed Opus RTP track. Signaling is intentionally minimal HTTP; it is not SIP over WSS or the production WebRTC gateway.

## Run

```sh
go run ./spikes/pion/cmd -http 127.0.0.1:18080 -max-peers 16 -session-timeout 5m
```

Open `http://127.0.0.1:18080`. Select **Connect microphone**, grant microphone permission, and use headphones; or select **Connect test tone** to validate the full browser media path without a device permission. Loopback HTTP is treated as a secure context by modern browsers. No public STUN or TURN service is configured, so this spike validates host-candidate connectivity only.

## Automated verification

```sh
go test -count=1 ./spikes/pion/...
go test -race -count=1 ./spikes/pion/...
```

The test performs the same HTTP offer/answer flow with a second Pion peer, waits for ICE/DTLS-SRTP connection, sends Opus RTP, verifies the echoed payload, and then closes every peer. HTTP bodies, peer count, connection lifetime, and all waits are bounded.
