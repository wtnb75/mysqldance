package source

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
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

// writePacket sends one MySQL protocol packet.
func writePacket(c net.Conn, seq byte, payload []byte) error {
	n := len(payload)
	_, err := c.Write(append([]byte{byte(n), byte(n >> 8), byte(n >> 16), seq}, payload...))
	return err
}

// readPacket reads one MySQL protocol packet and returns its payload.
func readPacket(c net.Conn) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return nil, err
	}
	buf := make([]byte, int(hdr[0])|int(hdr[1])<<8|int(hdr[2])<<16)
	_, err := io.ReadFull(c, buf)
	return buf, err
}

// answerOnce is a server that greets, logs in and answers pings on its first
// connection only, and accepts every later connection without a word: a server
// that stopped responding after Open made its first connection.
// accepted reports how many connections it has accepted so far.
func answerOnce(t *testing.T) (host string, port int, accepted func() int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	var count atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for n := 0; ; n++ {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			count.Store(int32(n + 1))
			if n == 0 {
				go serveHandshakeAndPings(c)
			}
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port, func() int { return int(count.Load()) }
}

// serveHandshakeAndPings sends the greeting (mysql_native_password, no TLS),
// accepts any login, and answers every later command with OK.
func serveHandshakeAndPings(c net.Conn) {
	greeting := []byte{10}
	greeting = append(greeting, "5.7.0\x00"...)
	greeting = append(greeting, 1, 0, 0, 0)                         // connection id
	greeting = append(greeting, "abcdefgh\x00"...)                  // auth data, part 1
	greeting = append(greeting, 0xff, 0xf7, 0x21, 2, 0, 0x0a, 0x00) // capabilities (low), charset, status, capabilities (high)
	greeting = append(greeting, 21)                                 // auth data length
	greeting = append(greeting, make([]byte, 10)...)                // reserved
	greeting = append(greeting, "ijklmnopqrst\x00"...)              // auth data, part 2
	greeting = append(greeting, "mysql_native_password\x00"...)
	ok := []byte{0, 0, 0, 2, 0, 0, 0}
	if writePacket(c, 0, greeting) != nil {
		return
	}
	if _, err := readPacket(c); err != nil { // the login
		return
	}
	if writePacket(c, 2, ok) != nil {
		return
	}
	for {
		if _, err := readPacket(c); err != nil { // a command; ping and quit need nothing else
			return
		}
		if writePacket(c, 1, ok) != nil {
			return
		}
	}
}

// ConnectTimeout covers the connections that --jobs adds too: a server that
// answers the first one and then stops answering must not hang Open on the
// others when no ReadTimeout is set.
func TestOpenConnectTimeoutCoversJobsConnections(t *testing.T) {
	host, port, accepted := answerOnce(t)
	cfg := Config{Host: host, Port: port, SSLMode: SSLDisabled, Jobs: 2, ConnectTimeout: 500 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err := Open(ctx, cfg, "db")
	if err == nil {
		t.Fatal("Open succeeded against a server that stopped answering")
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
	// The first connection was answered and reused; the one that timed out was
	// the second, so no more than two were made.
	// The accept loop counts a connection a moment after the client sees it
	// connected, so wait briefly for the second.
	for i := 0; i < 100 && accepted() < 2; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if n := accepted(); n != 2 {
		t.Errorf("the server accepted %d connections, want 2 (the answered one and the one that timed out)", n)
	}
}

// When the connect timeout cuts the connection short, the error names that
// timeout only, not the read timeout that is set as well.
func TestOpenConnectTimeoutDoesNotBlameReadTimeout(t *testing.T) {
	host, port := silentServer(t)
	cfg := Config{Host: host, Port: port, SSLMode: SSLDisabled, ConnectTimeout: 300 * time.Millisecond, ReadTimeout: time.Minute}
	_, err := Open(context.Background(), cfg, "db")
	if err == nil {
		t.Fatal("Open succeeded against a silent server")
	}
	if !strings.Contains(err.Error(), "connect timeout") || strings.Contains(err.Error(), "read timeout") {
		t.Errorf("Open: err = %v, want it to name the connect timeout and not the read timeout", err)
	}
}

// A connection made under the connect timeout is not tied to it: it stays
// usable after the timeout has elapsed.
func TestConnWithinConnUsableAfterTimeout(t *testing.T) {
	host, port, _ := answerOnce(t)
	mc := mysql.NewConfig()
	mc.Net, mc.Addr, mc.TLS = "tcp", net.JoinHostPort(host, strconv.Itoa(port)), nil
	connector, err := mysql.NewConnector(mc)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	c, err := connWithin(context.Background(), db, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	time.Sleep(400 * time.Millisecond)
	if err := c.PingContext(context.Background()); err != nil {
		t.Errorf("ping after the connect timeout elapsed: %v", err)
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
