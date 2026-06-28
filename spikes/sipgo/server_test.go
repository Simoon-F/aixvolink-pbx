package sipgospike

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

const networkTimeout = 3 * time.Second
const serverTimeout = 10 * time.Second

func TestServerRespondsToOptionsOverUDPAndTCP(t *testing.T) {
	addr := availableAddress(t)
	server, err := NewServer(Config{UDPAddr: addr, TCPAddr: addr})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), serverTimeout)
	result := make(chan error, 1)
	go func() { result <- server.Run(ctx) }()

	select {
	case <-server.Ready():
	case <-ctx.Done():
		t.Fatal("listeners did not become ready")
	}

	for _, network := range []string{"udp", "tcp"} {
		t.Run(network, func(t *testing.T) {
			response := exchangeOptions(ctx, t, network, addr)
			if !strings.HasPrefix(response, "SIP/2.0 200 OK\r\n") {
				t.Fatalf("unexpected response: %q", response)
			}
		})
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(networkTimeout):
		t.Fatal("server did not stop after cancellation")
	}
}

func availableAddress(t *testing.T) string {
	t.Helper()
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release reserved port: %v", err)
	}
	return addr
}

func exchangeOptions(ctx context.Context, t *testing.T, network, addr string) string {
	t.Helper()
	transport := strings.ToUpper(network)
	request := fmt.Sprintf("OPTIONS sip:health@%s SIP/2.0\r\nVia: SIP/2.0/%s 127.0.0.1:5099;rport;branch=z9hG4bK-phase0-%s\r\nMax-Forwards: 70\r\nFrom: <sip:probe@localhost>;tag=phase0\r\nTo: <sip:health@%s>\r\nCall-ID: phase0-%s@localhost\r\nCSeq: 1 OPTIONS\r\nContact: <sip:probe@127.0.0.1:5099>\r\nContent-Length: 0\r\n\r\n", addr, transport, network, addr, network)
	deadline := time.Now().Add(networkTimeout)

	if network == "udp" {
		dialer := net.Dialer{}
		conn, err := dialer.DialContext(ctx, "udp4", addr)
		if err != nil {
			t.Fatalf("dial UDP: %v", err)
		}
		t.Cleanup(func() {
			if err := conn.Close(); err != nil {
				t.Errorf("close UDP connection: %v", err)
			}
		})
		if err := conn.SetDeadline(deadline); err != nil {
			t.Fatalf("set UDP deadline: %v", err)
		}
		if _, err := io.WriteString(conn, request); err != nil {
			t.Fatalf("write UDP request: %v", err)
		}
		buf := make([]byte, 2048)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("read UDP response: %v", err)
		}
		return string(buf[:n])
	}

	dialer := net.Dialer{Timeout: networkTimeout}
	conn, err := dialer.DialContext(ctx, "tcp4", addr)
	if err != nil {
		t.Fatalf("dial TCP: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close TCP connection: %v", err)
		}
	})
	if err := conn.SetDeadline(deadline); err != nil {
		t.Fatalf("set TCP deadline: %v", err)
	}
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatalf("write TCP request: %v", err)
	}
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read TCP response: %v", err)
	}
	return string(buf[:n])
}
