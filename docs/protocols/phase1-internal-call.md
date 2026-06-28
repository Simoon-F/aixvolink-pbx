# Phase 1 SIP protocol coverage

The executable SIPp suite in `test/sipp` validates the Phase 1 UDP call-control boundary against a fresh in-memory server process.

Covered flows:

- REGISTER challenge, Digest authentication, and 200 response for extensions 1001 and 1002.
- INVITE, 100, 180, 200, ACK, caller BYE, and final 200.
- INVITE rejection with 486 and non-2xx ACK.
- INVITE, ringing, matching CANCEL, 200 to CANCEL, 487 to INVITE, and ACK.

Run with:

```sh
make sipp-test
```

The suite uses only loopback addresses and test-only credentials. SIPp must be installed. The scenarios intentionally contain no SDP because RTP/media negotiation starts in Phase 2.
