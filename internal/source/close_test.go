package source

import (
	"context"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// stallProxy forwards TCP to target until stall is set, after which it keeps
// the connections open but forwards nothing (a server that stopped answering).
type stallProxy struct {
	ln    net.Listener
	stall atomic.Bool
}

func newStallProxy(t *testing.T, target string) *stallProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &stallProxy{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.serve(c, target)
		}
	}()
	return p
}

func (p *stallProxy) serve(c net.Conn, target string) {
	up, err := net.Dial("tcp", target)
	if err != nil {
		_ = c.Close()
		return
	}
	pipe := func(dst, src net.Conn) {
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 && !p.stall.Load() {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		_ = dst.Close()
		_ = src.Close()
	}
	go pipe(up, c)
	pipe(c, up)
}

// Close must not wait forever for ROLLBACK when the server has stopped
// answering.
func TestCloseTimesOutWhenServerStalls(t *testing.T) {
	cfg, db, _ := testDB(t)
	if cfg.Socket != "" {
		t.Skip("needs a TCP connection to put a proxy in front")
	}
	p := newStallProxy(t, net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	_, portStr, _ := net.SplitHostPort(p.ln.Addr().String())
	cfg.Host = "127.0.0.1"
	cfg.Port, _ = strconv.Atoi(portStr)

	old := closeTimeout
	closeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { closeTimeout = old })

	s, err := Open(context.Background(), cfg, db)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	p.stall.Store(true)

	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Close returned nil, want the ROLLBACK timeout error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return while the server was stalled")
	}
}
