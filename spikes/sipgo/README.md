# sipgo UDP/TCP transaction spike

This isolated program proves that sipgo can bind UDP and TCP listeners, parse an RFC 3261 `OPTIONS` request, create a server transaction, and return `200 OK`. It is not a registrar or B2BUA.

## Run

```sh
go run ./spikes/sipgo/cmd -udp 127.0.0.1:15060 -tcp 127.0.0.1:15060
```

Send an `OPTIONS` request with a SIP client or SIPp to either loopback listener. Stop with Ctrl-C; cancellation closes both sockets and the sipgo user agent.

## Automated verification

```sh
go test -count=1 ./spikes/sipgo/...
go test -race -count=1 ./spikes/sipgo/...
```

The test starts both real listeners, sends one transaction over each transport, verifies `200 OK`, cancels the context, and asserts bounded shutdown. It uses only loopback addresses and a three-second deadline.
