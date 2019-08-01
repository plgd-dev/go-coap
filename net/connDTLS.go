package net

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type connDTLSData struct {
	data []byte
	err  error
}

type ConnDTLS struct {
	conn       net.Conn
	readDataCh chan connDTLSData
	doneCh     chan struct{}
	wg         sync.WaitGroup

	readDeadline atomic.Value
}

func (c *ConnDTLS) readLoop() {
	defer c.wg.Done()
	buf := make([]byte, 8192)
	for {
		n, err := c.conn.Read(buf)
		d := connDTLSData{err: err}
		if err == nil && n > 0 {
			d.data = append(d.data, buf[:n]...)
		}
		select {
		case c.readDataCh <- d:
			if err != nil {
				return
			}
		case <-c.doneCh:
			return
		}
	}
}

func NewConnDTLS(conn net.Conn) *ConnDTLS {
	c := ConnDTLS{
		conn:       conn,
		readDataCh: make(chan connDTLSData),
		doneCh:     make(chan struct{}),
	}
	c.wg.Add(1)
	go c.readLoop()
	return &c
}

type errS struct {
	error
	timeout   bool
	temporary bool
}

func (e errS) Timeout() bool {
	return e.timeout
}

func (e errS) Temporary() bool {
	return e.temporary
}

func (c *ConnDTLS) processData(b []byte, d connDTLSData) (n int, err error) {
	if d.err != nil {
		return 0, errS{
			error: d.err,
		}
	}
	if len(b) < len(d.data) {
		return 0, errS{
			error: fmt.Errorf("buffer is too small"),
		}
	}
	return copy(b, d.data), nil
}

func (c *ConnDTLS) Read(b []byte) (n int, err error) {
	var deadline time.Time
	v := c.readDeadline.Load()
	if v != nil {
		deadline = v.(time.Time)
	}
	if deadline.IsZero() {
		select {
		case d := <-c.readDataCh:
			return c.processData(b, d)
		}
	}

	select {
	case d := <-c.readDataCh:
		return c.processData(b, d)
	case <-time.After(deadline.Sub(time.Now())):
		return 0, errS{
			error:     fmt.Errorf(ioTimeout),
			temporary: true,
			timeout:   true,
		}
	}
}

func (c *ConnDTLS) Write(b []byte) (n int, err error) {
	return c.conn.Write(b)
}

func (c *ConnDTLS) Close() error {
	err := c.conn.Close()
	close(c.doneCh)
	c.wg.Wait()
	return err
}

func (c *ConnDTLS) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

func (c *ConnDTLS) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

func (c *ConnDTLS) SetDeadline(t time.Time) error {
	err := c.SetReadDeadline(t)
	if err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

func (c *ConnDTLS) SetReadDeadline(t time.Time) error {
	c.readDeadline.Store(t)
	return nil
}

func (c *ConnDTLS) SetWriteDeadline(t time.Time) error {
	return c.conn.SetWriteDeadline(t)
}
