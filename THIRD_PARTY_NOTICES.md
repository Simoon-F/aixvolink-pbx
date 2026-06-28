# Third-party notices

This file records direct Phase 0 runtime dependencies. AixvoLinkPBX Core is licensed under Apache-2.0; the dependencies below remain under their respective licenses. `go.sum` locks the full transitive dependency graph. The CI license audit checks the complete build graph; a formal legal review remains required before a production release.

| Component | Locked version | License | Use |
|---|---:|---|---|
| [emiago/sipgo](https://github.com/emiago/sipgo) | v1.4.0 | BSD-2-Clause | Copyright 2022 Emir Aganovic; SIP parsing, transport, and transactions |
| [emiago/diago](https://github.com/emiago/diago) | v0.29.0 | MPL-2.0 | Copyright 2024 Emir Aganovic; G.711 RTP/media capability validation |
| [pion/webrtc](https://github.com/pion/webrtc) | v4.2.15 | MIT | Copyright 2026 The Pion community; ICE, DTLS-SRTP, and browser WebRTC termination |
| [pion/rtp](https://github.com/pion/rtp) | v1.10.2 | MIT | Copyright 2015 The Pion community; RTP packet model used by media tests |

Security-fixed transitive version floors are explicitly locked for `golang.org/x/crypto v0.52.0`, `golang.org/x/net v0.55.0`, and `golang.org/x/sys v0.45.0`; each is BSD-3-Clause.

## MPL-2.0 handling

No Diago source file is copied or modified in this repository. Diago is consumed as an unmodified Go module. MPL-2.0 is file-level copyleft and permits distribution as part of a Larger Work under different terms, but recipients must retain MPL notices and be informed how to obtain the Source Code Form of the MPL-covered files. If a future change modifies MPL-covered files, those modified files and their corresponding source must remain available under MPL-2.0.

BSD-2-Clause and MIT dependencies require their copyright and license notices to be retained in source distributions and substantial binary distributions. Release packaging must therefore include this notice plus the authoritative upstream license texts.

The authoritative license texts are distributed by each upstream module and are available through the Go module cache after `go mod download`. The automated audit currently permits only Apache-2.0 for Core dependencies owned by this repository and MIT, BSD-2-Clause, BSD-3-Clause, or MPL-2.0 for external build dependencies.
