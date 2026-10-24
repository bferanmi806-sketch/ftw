package drivers

import (
	"net"
	"sync"
	"testing"
	"time"
)

// A server that accepts the connection and never answers the upgrade must
// not hold Open for long: Open runs inside driver_poll, and the driver's
// commands wait for the poll.
func TestGorillaWSOpenGivesUpOnASilentServer(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			c.Close()
		}
	})

	ws := NewGorillaWS("silent")
	start := time.Now()
	err = ws.Open("ws://"+ln.Addr().String()+"/hub", nil)
	took := time.Since(start)
	if err == nil {
		ws.Close()
		t.Fatal("Open succeeded against a server that never answered")
	}
	if took > wsDialTimeout+time.Second {
		t.Fatalf("Open waited %v for a silent server, want at most %v", took, wsDialTimeout)
	}
	if ws.IsOpen() {
		t.Fatal("a failed Open left the capability open")
	}
}
