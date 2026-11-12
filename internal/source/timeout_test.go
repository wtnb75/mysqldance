package source

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestOpenRejectsNegativeTimeouts(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"connect", Config{ConnectTimeout: -time.Second}, "ConnectTimeout"},
		{"read", Config{ReadTimeout: -time.Second}, "ReadTimeout"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Open(context.Background(), tt.cfg, "db")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Open: err = %v, want an error naming %s", err, tt.want)
			}
		})
	}
}

// silentServer accepts connections and never answers, like a server that
// stopped responding after the TCP handshake.
func silentServer(t *testing.T) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	var conns []net.Conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns = append(conns, c)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
		for _, c := range conns {
			_ = c.Close()
		}
	})
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

// A server that never answers must make Open fail after ReadTimeout instead of
// waiting forever for the handshake.
func TestOpenReadTimeout(t *testing.T) {
	host, port := silentServer(t)
	cfg := Config{Host: host, Port: port, SSLMode: SSLDisabled, ReadTimeout: 300 * time.Millisecond,
		ConnectTimeout: DefaultConnectTimeout} // what the command line sets
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err := Open(ctx, cfg, "db")
	if err == nil {
		t.Fatal("Open succeeded against a silent server")
	}
	if ctx.Err() != nil {
		t.Fatalf("Open did not give up on its own: %v", err)
	}
	// The driver reports a read timeout only as "invalid connection"; the error
	// must say what happened.
	if !strings.Contains(err.Error(), "read timeout") {
		t.Errorf("Open: err = %v, want it to name the read timeout", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Open took %v, want about the read timeout", d)
	}
}

// ConnectTimeout covers making the connection, not only the TCP dial: a server
// that accepts the connection but never sends its greeting must not hang Open
// when no ReadTimeout is set.
func TestOpenConnectTimeoutCoversHandshake(t *testing.T) {
	host, port := silentServer(t)
	cfg := Config{Host: host, Port: port, SSLMode: SSLDisabled, ConnectTimeout: 300 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err := Open(ctx, cfg, "db")
	if err == nil {
		t.Fatal("Open succeeded against a silent server")
	}
	if ctx.Err() != nil {
		t.Fatalf("Open did not give up on its own: %v", err)
	}
	if !strings.Contains(err.Error(), "connect timeout") {
		t.Errorf("Open: err = %v, want it to name the connect timeout", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Open took %v, want about the connect timeout", d)
	}
}

// A host that does not answer the TCP handshake must make Open fail after
// ConnectTimeout. 192.0.2.0/24 (TEST-NET-1) is never routed; where the
// sandbox rejects it at once instead of dropping packets, there is nothing to
// time out and the test skips.
func TestOpenConnectTimeout(t *testing.T) {
	cfg := Config{Host: "192.0.2.1", Port: 3306, SSLMode: SSLDisabled, ConnectTimeout: 300 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err := Open(ctx, cfg, "db")
	if err == nil {
		t.Fatal("Open succeeded against an unroutable address")
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Skipf("the address is rejected rather than dropped here: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("Open did not give up on its own: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Open took %v, want about the connect timeout", d)
	}
}
