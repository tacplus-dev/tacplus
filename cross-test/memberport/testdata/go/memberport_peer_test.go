// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package memberport

// memberport-peer (tacplus#212, #321, #322): the test fixture that stands in
// for the black-box protocol behind a member port, the Go twin of
// MemberportPeer.java. Test code only, never shipped in a service. It talks
// raw TCP. Every byte it sends is a seeded pseudo-random stream, and the other
// side checks what it receives byte for byte as it arrives, so a dropped,
// duplicated, reordered or corrupted byte fails at the first wrong offset.
//
// A peerServer accepts and either echoes (echoMode) or exchanges (pushMode):
// it sends its own seeded stream while it reads and checks the client's, in
// both directions at once. exchange is the client side.

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// peerStream is the seeded stream: byte i of seed s is the same however it is
// chunked. It is java.util.SplittableRandom(seed).nextInt(256), byte by byte,
// so a Go peer and MemberportPeer.java check each other's streams (the
// interop leg).
type peerStream struct{ state uint64 }

func newPeerStream(seed int64) *peerStream { return &peerStream{state: uint64(seed)} }

// next is SplittableRandom.nextInt(): mix32 of the next seed.
func (s *peerStream) next() uint64 {
	s.state += 0x9E3779B97F4A7C15
	z := s.state
	z = (z ^ (z >> 33)) * 0x62A9D9ED799705F5
	return ((z ^ (z >> 28)) * 0xCB24D0A5C88C35B3) >> 32
}

func (s *peerStream) fill(buf []byte) {
	for i := range buf {
		buf[i] = byte(s.next()) // nextInt(256): the low 8 bits
	}
}

// check checks received bytes against the stream; it returns the first wrong index or -1.
func (s *peerStream) check(buf []byte) int {
	for i, b := range buf {
		if b != byte(s.next()) {
			return i
		}
	}
	return -1
}

// peerResult is what one side of a connection saw.
type peerResult struct {
	received      int64
	firstMismatch int64
	err           string
}

func (r peerResult) ok(expected int64) bool {
	return r.err == "" && r.firstMismatch < 0 && r.received == expected
}

func (r peerResult) String() string {
	return fmt.Sprintf("result{received=%d, firstMismatch=%d, err=%q}", r.received, r.firstMismatch, r.err)
}

// wasReset: the black-box side saw its connection reset, on a read, or on a
// write when the writer met the RST first.
func (r peerResult) wasReset() bool {
	return strings.Contains(r.err, "reset") || strings.Contains(r.err, "broken pipe")
}

func describe(err error) string {
	switch {
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset: " + err.Error()
	case errors.Is(err, syscall.EPIPE):
		return "broken pipe: " + err.Error()
	}
	return err.Error()
}

// peerStall bounds a peer's stream by its progress, never by its total time
// (#919): a read or write that moves no byte for this long fails, and its
// error names the bound. A slowed stream runs for as long as it moves. Zero
// is the default, 60 seconds; a test may store a shorter one.
var peerStall atomic.Int64

func peerStallBound() time.Duration {
	if d := time.Duration(peerStall.Load()); d > 0 {
		return d
	}
	return 60 * time.Second
}

// stallConn puts each read and write under the stall bound.
type stallConn struct {
	*net.TCPConn
	bound time.Duration
}

func (c stallConn) Read(b []byte) (int, error) {
	_ = c.SetReadDeadline(time.Now().Add(c.bound))
	n, err := c.TCPConn.Read(b)
	return n, c.stalled(err)
}

func (c stallConn) Write(b []byte) (int, error) {
	_ = c.SetWriteDeadline(time.Now().Add(c.bound))
	n, err := c.TCPConn.Write(b)
	return n, c.stalled(err)
}

func (c stallConn) stalled(err error) error {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Errorf("no byte moved in %v, the peer's stall bound: %w", c.bound, err)
	}
	return err
}

// peerSend sends n bytes of seed's stream in seeded chunk sizes, then half-closes.
func peerSend(c interface {
	io.Writer
	CloseWrite() error
}, seed, n int64, maxChunk int, pause time.Duration) error {
	stream := newPeerStream(seed)
	chunks := newPeerStream(seed ^ 0x5DEECE66D)
	buf := make([]byte, maxChunk)
	for sent := int64(0); sent < n; {
		size := min(n-sent, 1+int64(chunks.next()%uint64(maxChunk)))
		stream.fill(buf[:size])
		if _, err := c.Write(buf[:size]); err != nil {
			return err
		}
		sent += size
		if pause > 0 {
			time.Sleep(pause)
		}
	}
	return c.CloseWrite()
}

// peerReceive reads to EOF, checking every byte against seed's stream; after
// each read it pauses while pause() is positive, re-read as it lasts, so
// setting it to 0 wakes a stalled reader.
func peerReceive(c net.Conn, seed int64, pause func() time.Duration) peerResult {
	stream := newPeerStream(seed)
	buf := make([]byte, 16*1024)
	var received int64
	for {
		n, err := c.Read(buf)
		if n > 0 {
			if bad := stream.check(buf[:n]); bad >= 0 {
				return peerResult{received: received + int64(n), firstMismatch: received + int64(bad)}
			}
			received += int64(n)
		}
		if err == io.EOF {
			return peerResult{received: received, firstMismatch: -1}
		}
		if err != nil {
			return peerResult{received: received, firstMismatch: -1, err: describe(err)}
		}
		until := time.Now().Add(pause())
		for pause() > 0 && time.Now().Before(until) {
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func noPause() time.Duration { return 0 }

// exchange is the client side: it connects to address, sends sendBytes of
// sendSeed while it reads and checks expectSeed's stream.
func exchange(address net.Addr, sendSeed, sendBytes, expectSeed int64) peerResult {
	return exchangeWith(address, sendSeed, sendBytes, expectSeed, 16*1024, 0, 0)
}

func exchangeWith(address net.Addr, sendSeed, sendBytes, expectSeed int64, maxChunk int, sendPause, readPause time.Duration) peerResult {
	raw, err := net.DialTimeout("tcp", address.String(), 10*time.Second)
	if err != nil {
		return peerResult{firstMismatch: -1, err: describe(err)}
	}
	c := stallConn{raw.(*net.TCPConn), peerStallBound()}
	defer c.Close()
	sendErr := make(chan error, 1)
	go func() { sendErr <- peerSend(c, sendSeed, sendBytes, maxChunk, sendPause) }()
	r := peerReceive(c, expectSeed, func() time.Duration { return readPause })
	// The send ends by itself: each of its writes is under the stall bound.
	if err := <-sendErr; r.err == "" && err != nil {
		r.err = "send failed: " + describe(err)
	}
	return r
}

type peerMode int

const (
	echoMode peerMode = iota
	pushMode
)

// peerServer is the server side: it echoes, or exchanges seeded streams with every client.
type peerServer struct {
	lis        *net.TCPListener
	mode       peerMode
	pushSeed   int64
	pushBytes  int64
	expectSeed int64
	pushChunk  int
	pushPause  time.Duration
	// stall is peerStallBound() when the server started, for its pushes.
	stall     time.Duration
	readPause atomic.Int64 // nanoseconds
	// accepted counts connections: zero means nothing ever reached the protocol.
	accepted      atomic.Int32
	bytesReceived atomic.Int64
	mu            sync.Mutex
	results       map[int32]peerResult
}

func newPeerServer(t *testing.T, mode peerMode, pushSeed, pushBytes, expectSeed int64, pushChunk int, pushPause time.Duration) *peerServer {
	t.Helper()
	lis, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("peer server: %v", err)
	}
	s := &peerServer{lis: lis, mode: mode, pushSeed: pushSeed, pushBytes: pushBytes, expectSeed: expectSeed,
		pushChunk: pushChunk, pushPause: pushPause, stall: peerStallBound(), results: make(map[int32]peerResult)}
	t.Cleanup(func() { _ = lis.Close() })
	go s.acceptLoop()
	return s
}

func (s *peerServer) address() string { return s.lis.Addr().String() }

func (s *peerServer) pause() time.Duration { return time.Duration(s.readPause.Load()) }

func (s *peerServer) result(id int32) (peerResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.results[id]
	return r, ok
}

func (s *peerServer) resultCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.results)
}

func (s *peerServer) clearResults() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results = make(map[int32]peerResult)
}

func (s *peerServer) put(id int32, r peerResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[id] = r
}

func (s *peerServer) acceptLoop() {
	for {
		c, err := s.lis.AcceptTCP()
		if err != nil {
			return
		}
		id := s.accepted.Add(1)
		go s.serve(id, c)
	}
}

func (s *peerServer) serve(id int32, c *net.TCPConn) {
	defer c.Close()
	if s.mode == echoMode {
		buf := make([]byte, 16*1024)
		for {
			n, err := c.Read(buf)
			if n > 0 {
				s.bytesReceived.Add(int64(n))
				if _, werr := c.Write(buf[:n]); werr != nil {
					s.put(id, peerResult{firstMismatch: -1, err: describe(werr)})
					return
				}
			}
			if err == io.EOF {
				_ = c.CloseWrite()
				s.put(id, peerResult{received: -1, firstMismatch: -1})
				return
			}
			if err != nil {
				s.put(id, peerResult{firstMismatch: -1, err: describe(err)})
				return
			}
			if p := s.pause(); p > 0 {
				time.Sleep(p)
			}
		}
	}
	pushErr := make(chan error, 1)
	go func() {
		pushErr <- peerSend(stallConn{c, s.stall}, s.pushSeed, s.pushBytes, s.pushChunk, s.pushPause)
	}()
	r := peerReceive(c, s.expectSeed, s.pause)
	s.bytesReceived.Add(r.received)
	// The push ends by itself, under the stall bound, however long it runs
	// past the read side. Otherwise the reader's result records the
	// connection's fate; a stalled push is the server's own.
	if err := <-pushErr; r.err == "" && errors.Is(err, os.ErrDeadlineExceeded) {
		r.err = "push failed: " + describe(err)
	}
	s.put(id, r)
}

// A slowed peer whose stream outlasts the stall bound many times over still
// delivers every byte: the bound is on progress, never on the stream's total
// time (#919). The client sends its few bytes at once and reads slowly while
// the server pushes slowly, so the server's push outlives its read side too.
func TestMemberPortPeerBoundsAStreamByProgressNotWallTime(t *testing.T) {
	defer peerStall.Store(0)
	peerStall.Store(int64(300 * time.Millisecond))
	server := newPeerServer(t, pushMode, 61, 40*1024, 62, 1024, 50*time.Millisecond)
	addr, err := net.ResolveTCPAddr("tcp", server.address())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r := exchangeWith(addr, 62, 4096, 61, 4096, 0, 20*time.Millisecond)
	elapsed := time.Since(start)
	if !r.ok(40 * 1024) {
		t.Fatalf("a slowed stream, %v long against a %v stall bound: %v", elapsed, peerStallBound(), r)
	}
	if elapsed < 3*peerStallBound() {
		t.Fatalf("the stream must outlast the stall bound several times over, took %v", elapsed)
	}
	waitFor(t, "the server's result", 10*time.Second, func() bool { return server.resultCount() == 1 })
	if sr, _ := server.result(1); !sr.ok(4096) {
		t.Fatalf("server: %v", sr)
	}
}

// A peer that stops making progress is cut at the stall bound, and the error
// says so.
func TestMemberPortPeerNamesTheStallBound(t *testing.T) {
	defer peerStall.Store(0)
	peerStall.Store(int64(300 * time.Millisecond))
	lis, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	go func() {
		// Accepts, then neither reads nor writes.
		c, err := lis.Accept()
		if err == nil {
			defer c.Close()
			time.Sleep(10 * time.Second)
		}
	}()
	start := time.Now()
	r := exchangeWith(lis.Addr(), 1, 1024, 2, 1024, 0, 0)
	if !strings.Contains(r.err, "stall bound") {
		t.Fatalf("a stalled peer: %v", r)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("a stalled peer is cut at the stall bound, took %v", elapsed)
	}
}
