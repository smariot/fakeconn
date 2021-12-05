package fakeconn

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

type fakeAddress string

var (
	localAddr  net.Addr = fakeAddress("local")
	remoteAddr net.Addr = fakeAddress("remote")
)

func (a fakeAddress) Network() string {
	return "fake"
}

func (a fakeAddress) String() string {
	return string(a)
}

type Opts struct {
	LocalAddr        net.Addr
	RemoteAddr       net.Addr
	FailBlockedReads bool
}

type ReplayConn struct {
	m sync.Mutex
	c sync.Cond

	failBlockedReads bool

	localAddr, remoteAddr net.Addr

	lineNo int

	readDeadline time.Time
	readerCount  int
	readBuf      []byte
	readPos      int
	readErr      error

	writeDeadline time.Time
	writerCount   int
	writeBuf      []byte
	writePos      int
	writeErr      error

	closeErr error

	wg     sync.WaitGroup
	closed bool
}

// SetAddr sets the addresses reported by the connection.
//
// Defaults to "local" and "remote" on the network "fake".
func (rp *ReplayConn) SetAddr(local, remote net.Addr) *ReplayConn {
	rp.m.Lock()
	defer rp.m.Unlock()
	rp.localAddr, rp.remoteAddr = local, remote
	return rp
}

func (rp *ReplayConn) FailBlockedReads(fail bool) *ReplayConn {
	rp.m.Lock()
	defer rp.m.Unlock()
	rp.failBlockedReads = fail
	return rp
}

func Replay(r io.Reader) *ReplayConn {
	rp := ReplayConn{
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
	}
	rp.c.L = &rp.m

	rp.wg.Add(2)
	go rp.handleRead(r)
	go rp.handleDeadline()

	return &rp
}

type streamLine struct {
	dir  string
	data []byte
	err  error
}

func parseErr(data []byte) (e error, err error) {
	if bytes.HasPrefix(data, []byte("ERR ")) {
		var msg []byte
		msg, err = unescape(data[4:])
		if err != nil {
			return nil, err
		}
		return &replayErr{errors.New(string(msg))}, nil
	}

	if bytes.Equal(data, []byte("ERR")) {
		return errGeneric, nil
	}

	if bytes.Equal(data, []byte("EOF")) {
		return io.EOF, nil
	}

	return nil, fmt.Errorf("%w: %q", errBadErr, data)
}

func parseLine(s *bufio.Scanner) (line streamLine, err error) {
	line.dir = "xx"

	if !s.Scan() {
		err = s.Err()
		if err == nil {
			err = io.EOF
		} else {
			err = &replayErr{err}
		}
	}

	data := s.Bytes()

	if len(data) == 0 || data[0] == '#' {
		return
	}

	if len(data) < 3 {
		err = fmt.Errorf("%w: %q", errBadPrefix, data)
		return
	}

	prefix := string(data[:3])

	switch prefix {
	case "<- ", "-> ":
		line.dir = prefix[:2]
		line.data, err = unescape(data[3:])
	case "<-!", "->!", "  !":
		line.dir = prefix[:2]
		line.err, err = parseErr(data[3:])
	default:
		err = fmt.Errorf("%w: %q", errBadPrefix, data[:3])
	}

	return
}

func (rp *ReplayConn) setErrs(read, write, close error) {
	if read != nil {
		rp.readPos, rp.readBuf = 0, nil
		if rp.readErr == nil {
			rp.readErr = read
		}
	}

	if write != nil {
		rp.writePos, rp.writeBuf = 0, nil
		if rp.writeErr == nil {
			rp.writeErr = write
		}
	}

	if close != nil && rp.closeErr == nil {
		rp.closeErr = close
	}
}

func (rp *ReplayConn) handleRead(r io.Reader) {
	defer rp.wg.Done()

	s := bufio.NewScanner(r)
	for {
		line, err := parseLine(s)

		rp.m.Lock()

		for !rp.closed && !(rp.readPos == len(rp.readBuf) && rp.writePos == len(rp.writeBuf)) {
			rp.c.Wait()
		}

		rp.lineNo++

		if err != nil {
			rp.closed = true
			if err == io.EOF {
				rp.setErrs(io.EOF, errBrokenPipe, nil)
			} else {
				rp.setErrs(errBrokenPipe, errBrokenPipe, fmt.Errorf("line %d: %w", rp.lineNo, err))
			}

			rp.c.Broadcast()
			rp.m.Unlock()
			return
		}

		if rp.closed {
			rp.setErrs(errClosed, errClosed, fmt.Errorf("line %d: %w", rp.lineNo, errPrematureClose))
			rp.c.Broadcast()
			rp.m.Unlock()
			return
		}

		switch line.dir {
		case "<-":
			rp.readPos, rp.readBuf, rp.readErr = 0, line.data, line.err
		case "->":
			rp.writePos, rp.writeBuf, rp.writeErr = 0, line.data, line.err
		case "  ":
			rp.setErrs(errBrokenPipe, errBrokenPipe, line.err)
		}
		rp.c.Broadcast()
		rp.m.Unlock()
	}
}

func (rp *ReplayConn) handleDeadlineUpdates(deadlines <-chan time.Time) {
	defer rp.wg.Done()

	t := time.NewTimer(0)
	tc := t.C

	for {
		select {
		case deadline, ok := <-deadlines:
			if tc != nil && !t.Stop() {
				<-t.C
			}

			tc = nil

			if !ok {
				return
			}

			if !deadline.IsZero() {
				t.Reset(time.Until(deadline))
				tc = t.C
			}

		case <-tc:
			rp.c.Broadcast()
			tc = nil
		}
	}
}

func (rp *ReplayConn) handleDeadline() {
	defer rp.wg.Done()

	deadlines := make(chan time.Time)
	defer close(deadlines)

	rp.wg.Add(1)
	go rp.handleDeadlineUpdates(deadlines)

	var lastDeadline time.Time

	rp.m.Lock()
	for {
		if rp.closed {
			rp.m.Unlock()
			return
		}

		nextDeadline := rp.readDeadline

		if nextDeadline.IsZero() || (!rp.writeDeadline.IsZero() && rp.writeDeadline.Before(nextDeadline)) {
			nextDeadline = rp.writeDeadline
		}

		if nextDeadline.Equal(lastDeadline) {
			rp.c.Wait()
		} else {
			rp.m.Unlock()
			deadlines <- nextDeadline
			lastDeadline = nextDeadline
			rp.m.Lock()
		}
	}
}

func (rp *ReplayConn) opErr(op string, err error) error {
	if err == io.EOF {
		// Don't wrap EOF
		return io.EOF
	}

	return &net.OpError{
		Op:     op,
		Net:    rp.localAddr.Network(),
		Source: rp.localAddr,
		Addr:   rp.remoteAddr,
		Err:    err,
	}
}

func (rp *ReplayConn) Read(b []byte) (n int, err error) {
	rp.m.Lock()
	defer rp.m.Unlock()

	rp.readerCount++
	defer func() {
		rp.readerCount--
	}()

	for {
		if !rp.readDeadline.IsZero() && !rp.readDeadline.After(time.Now()) {
			err = rp.opErr("read", os.ErrDeadlineExceeded)
			return
		}

		if rp.readErr != nil {
			err = rp.opErr("write", rp.readErr)
			return
		}

		if n == len(b) {
			return
		}

		if rp.readPos != len(rp.readBuf) {
			l := copy(b[n:], rp.readBuf[rp.readPos:])
			n += l
			rp.readPos += l
			rp.c.Broadcast()
			return
		}

		if rp.writePos != len(rp.writeBuf) && rp.failBlockedReads {
			e := fmt.Errorf("line %d: %w: waiting for a write of %q", rp.lineNo, errBadRead, rp.writeBuf[rp.writePos:])
			rp.setErrs(errBrokenPipe, errBrokenPipe, e)
			err = rp.opErr("read", e)
			return
		}

		rp.c.Wait()
	}
}

func (rp *ReplayConn) Write(b []byte) (n int, err error) {
	rp.m.Lock()
	defer rp.m.Unlock()

	rp.writerCount++
	defer func() {
		rp.writerCount--
	}()

	for {
		if rp.closed && n != len(b) {
			e := fmt.Errorf("line %d: %w: data=%q expected=%q", rp.lineNo, errBadWrite, b, b[:n])
			rp.setErrs(errBrokenPipe, errBrokenPipe, e)
			err = rp.opErr("write", e)
			return
		}

		if !rp.writeDeadline.IsZero() && !rp.writeDeadline.After(time.Now()) {
			err = rp.opErr("write", os.ErrDeadlineExceeded)
			return
		}

		if rp.writeErr != nil {
			err = rp.opErr("write", rp.writeErr)
			return
		}

		if n == len(b) {
			return
		}

		if rp.writePos != len(rp.writeBuf) {
			l := len(b) - n
			if l > len(rp.writeBuf)-rp.writePos {
				l = len(rp.writeBuf) - rp.writePos
			}

			if !bytes.Equal(b[n:n+l], rp.writeBuf[rp.writePos:rp.writePos+l]) {
				e := fmt.Errorf("line %d: %w: data=%q expected=%q", rp.lineNo, errBadWrite, append(rp.writeBuf[:rp.writePos:rp.writePos], b[n:]...), rp.writeBuf)
				rp.setErrs(errBrokenPipe, errBrokenPipe, e)
				err = rp.opErr("write", e)
				return
			}

			n += l
			rp.writePos += l

			if rp.writePos == len(rp.writeBuf) {
				rp.c.Broadcast()
			}

			if n == len(b) {
				return
			}
		}

		rp.c.Wait()
	}
}

func (rp *ReplayConn) Close() error {
	rp.m.Lock()
	rp.closed = true
	rp.c.Broadcast()
	rp.m.Unlock()
	rp.wg.Wait()
	return rp.closeErr
}

func (rp *ReplayConn) LocalAddr() net.Addr {
	rp.m.Lock()
	defer rp.m.Unlock()
	return rp.localAddr
}

func (rp *ReplayConn) RemoteAddr() net.Addr {
	rp.m.Lock()
	defer rp.m.Unlock()
	return rp.remoteAddr
}

func (rp *ReplayConn) SetDeadline(t time.Time) error {
	rp.m.Lock()
	defer rp.m.Unlock()

	rp.readDeadline = t
	rp.writeDeadline = t
	rp.c.Broadcast()
	return nil
}

func (rp *ReplayConn) SetReadDeadline(t time.Time) error {
	rp.m.Lock()
	defer rp.m.Unlock()

	rp.readDeadline = t
	rp.c.Broadcast()
	return nil
}

func (rp *ReplayConn) SetWriteDeadline(t time.Time) error {
	rp.m.Lock()
	defer rp.m.Unlock()

	rp.readDeadline = t
	rp.c.Broadcast()
	return nil
}

func formatBuffer(pos int, buf []byte) string {
	return fmt.Sprintf("┆%s‸%s┆ %d/%d", escape(buf[:pos]), escape(buf[pos:]), pos, len(buf))
}

func (rp *ReplayConn) DebugStatus() string {
	var b strings.Builder
	now := time.Now()

	rp.m.Lock()
	defer rp.m.Unlock()

	fmt.Fprintf(&b, " == connection status: ==\n")
	fmt.Fprintf(&b, "current line:   %d\n", rp.lineNo)

	var deadline string
	switch {
	case rp.readDeadline.IsZero():
		deadline = "unset"
	case rp.readDeadline.Before(now):
		deadline = "expired"
	default:
		deadline = rp.readDeadline.Sub(now).String()
	}

	fmt.Fprintf(&b, "read deadline:  %s\n", deadline)
	fmt.Fprintf(&b, "reader count:   %d\n", rp.readerCount)
	fmt.Fprintf(&b, "read buffer:   %s\n", formatBuffer(rp.readPos, rp.readBuf))
	fmt.Fprintf(&b, "read error:     %v\n", rp.readErr)
	fmt.Fprintf(&b, "\n")

	switch {
	case rp.writeDeadline.IsZero():
		deadline = "unset"
	case rp.writeDeadline.Before(now):
		deadline = "expired"
	default:
		deadline = rp.writeDeadline.Sub(now).String()
	}

	fmt.Fprintf(&b, "write deadline: %s\n", deadline)
	fmt.Fprintf(&b, "writer count:   %d\n", rp.writerCount)
	fmt.Fprintf(&b, "write buffer:  %s\n", formatBuffer(rp.writePos, rp.writeBuf))
	fmt.Fprintf(&b, "write error:    %v\n", rp.writeErr)
	fmt.Fprintf(&b, "\n")

	fmt.Fprintf(&b, "closed:         %v\n", rp.closed)
	fmt.Fprintf(&b, "close error:    %v\n", rp.closeErr)

	return b.String()
}
