package fakeconn

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
)

type recordListen struct {
	net.Listener

	m sync.Mutex
	w io.Writer
}

func (rl *recordListen) Accept() (c net.Conn, err error) {
	c, err = rl.Listener.Accept()

	rl.m.Lock()
	defer rl.m.Unlock()
	if rl.w != nil && err == nil {
		c = Record(c, rl.w)
		rl.w = nil
	}

	return
}

type recorder struct {
	net.Conn

	m   sync.Mutex
	w   io.Writer
	dir string
	buf []byte
}

// RecordListen returns a wrapper around listener that will invoke Record on its first accepted connection.
func RecordListen(l net.Listener, w io.Writer) net.Listener {
	return &recordListen{Listener: l, w: w}
}

// Record returns a wrapper around c that will record any data sent or received to w.
func Record(c net.Conn, w io.Writer) net.Conn {
	return &recorder{Conn: c, w: w}
}

func (r *recorder) emit(dir string, data []byte) (err error) {
	if len(data) > 0 {
		_, err = fmt.Fprintf(r.w, "%s %s\n", dir, escape(data))
	}
	return
}

func (r *recorder) flush() (err error) {
	err = r.emit(r.dir, r.buf)
	r.dir, r.buf = "", r.buf[:0]
	return
}

func (r *recorder) append(dir string, data []byte) (err error) {
	if len(data) == 0 {
		return
	}

	if dir != r.dir {
		err = r.flush()
		if err != nil {
			return
		}
	}

	r.dir, r.buf = dir, append(r.buf, data...)

	for {
		i := chooseEscapeBreak(r.buf, 120)
		if i == -1 {
			break
		}
		err = r.emit(dir, r.buf[:i])
		r.buf = r.buf[:copy(r.buf, r.buf[i:])]
		if err != nil {
			return
		}
	}

	return nil
}

func (r *recorder) appendErr(dir string, e error) (err error) {
	if e == nil {
		return
	}

	if errors.Is(e, os.ErrDeadlineExceeded) {
		// This is presumably just to wake up an otherwise blocked
		// connection, and not an error we need a report.
		return
	}

	var netErr *net.OpError
	if errors.As(e, &netErr) && netErr.Err.Error() == "use of closed network connection" {
		// Error comes from internal/poll, and as far as I can tell, isn't exported.
		// This is also presumably just to wake up an otherwise blocked connection,
		// and not an error we need to report.
		return
	}

	err = r.flush()
	if err != nil {
		return
	}

	switch e {
	case io.EOF:
		_, err = fmt.Fprintf(r.w, "%s!EOF\n", dir)
	default:
		_, err = fmt.Fprintf(r.w, "%s!ERR %s\n", dir, escape([]byte(e.Error())))
	}

	return
}

func (r *recorder) Read(b []byte) (n int, err error) {
	n, err = r.Conn.Read(b)

	r.m.Lock()
	defer r.m.Unlock()

	if err2 := r.append("<-", b[:n]); err == nil {
		err = err2
	}

	if err2 := r.appendErr("<-", err); err == nil {
		err = err2
	}

	return
}

func (r *recorder) Write(b []byte) (n int, err error) {
	n, err = r.Conn.Write(b)

	r.m.Lock()
	defer r.m.Unlock()

	if err2 := r.append("->", b[:n]); err == nil {
		err = err2
	}

	if err2 := r.appendErr("->", err); err == nil {
		err = err2
	}

	return
}

func (r *recorder) Close() (err error) {
	err = r.Conn.Close()

	r.m.Lock()
	defer r.m.Unlock()

	if err2 := r.flush(); err == nil {
		err = err2
	}

	r.appendErr("  ", err)

	return
}
