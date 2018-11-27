package coap

import (
	"bytes"
	"log"
	"net"
	"sync/atomic"
	"time"
	"github.com/flynn/noise"
	// "runtime/debug"
)

type writeReq interface {
	sendResp(err error, timeout time.Duration)
	waitResp(timeout time.Duration) error
	data() Message
}

type writeReqBase struct {
	req      Message
	respChan chan error // channel must have size 1 for non-blocking write to channel
}

func (wreq *writeReqBase) sendResp(err error, timeout time.Duration) {
	select {
	case wreq.respChan <- err:
		return
	default:
		log.Fatal("Exactly one error can be send as resp. This is err.")
	}
}

func (wreq *writeReqBase) waitResp(timeout time.Duration) error {
	select {
	case err := <-wreq.respChan:
		return err
	case <-time.After(timeout):
		return ErrTimeout
	}
}

func (wreq *writeReqBase) data() Message {
	return wreq.req
}

type writeReqTCP struct {
	writeReqBase
}

type writeReqUDP struct {
	writeReqBase
	sessionData *SessionUDPData
}

// Conn represents the connection
type Conn interface {
	// LocalAddr get local address of the connection
	LocalAddr() net.Addr
	// RemoteAddr get peer address of the connection
	RemoteAddr() net.Addr
	// Close close the connection
	Close() error

	SetNoiseState(ns *NoiseState)

	write(w writeReq, timeout time.Duration) error
}

type connWriter interface {
	writeHandler(srv *Server) bool
	writeEndHandler(timeout time.Duration) bool
	sendFinish(timeout time.Duration)

	writeHandlerWithFunc(srv *Server, writeFunc func(srv *Server, wreq writeReq) error) bool
}

type connBase struct {
	writeChan chan writeReq
	closeChan chan bool
	finChan   chan bool
	closed    int32
	ns		  *NoiseState
}

func (conn *connBase) SetNoiseState(ns *NoiseState) {
	// log.Printf("Setting ns %p on conn %p", ns.Hs, conn)
	conn.ns = ns
}

func (conn *connBase) finishWrite() {
	if !atomic.CompareAndSwapInt32(&conn.closed, conn.closed, 1) {
		return
	}
	conn.closeChan <- true
	<-conn.finChan
}

func (conn *connBase) writeHandlerWithFunc(srv *Server, writeFunc func(srv *Server, wreq writeReq) error) bool {
	select {
	case wreq := <-conn.writeChan:
		wreq.sendResp(writeFunc(srv, wreq), srv.syncTimeout())
		return true
	case <-conn.closeChan:
		return false
	}
}

func (conn *connBase) sendFinish(timeout time.Duration) {
	select {
	case conn.finChan <- true:
	case <-time.After(timeout):
		log.Fatal("Client cannot recv start: Timeout")
	}
}

func (conn *connBase) writeEndHandler(timeout time.Duration) bool {
	select {
	case wreq := <-conn.writeChan:
		wreq.sendResp(ErrConnectionClosed, timeout)
		return true
	default:
		return false
	}
}

func (conn *connBase) write(w writeReq, timeout time.Duration) error {
	if atomic.LoadInt32(&conn.closed) > 0 {
		return ErrConnectionClosed
	}
	select {
	case conn.writeChan <- w:
		return w.waitResp(timeout)
	case <-time.After(timeout):
		return ErrTimeout
	}
}

type connTCP struct {
	connBase
	connection net.Conn // i/o connection if TCP was used
	num        int32
}

func (conn *connTCP) LocalAddr() net.Addr {
	return conn.connection.LocalAddr()
}

func (conn *connTCP) RemoteAddr() net.Addr {
	return conn.connection.RemoteAddr()
}

func (conn *connTCP) Close() error {
	conn.finishWrite()
	return conn.connection.Close()
}

func (conn *connTCP) writeHandler(srv *Server) bool {
	return conn.writeHandlerWithFunc(srv, func(srv *Server, wreq writeReq) error {
		data := wreq.data()
		wr := srv.acquireWriter(conn.connection)
		defer srv.releaseWriter(wr)
		writeTimeout := srv.writeTimeout()
		conn.connection.SetWriteDeadline(time.Now().Add(writeTimeout))
		err := data.MarshalBinary(wr)
		if err != nil {
			return err
		}
		wr.Flush()
		return nil
	})
}

type connUDP struct {
	connBase
	connection *net.UDPConn // i/o connection if UDP was used
}

func (conn *connUDP) LocalAddr() net.Addr {
	return conn.connection.LocalAddr()
}

func (conn *connUDP) RemoteAddr() net.Addr {
	return conn.connection.RemoteAddr()
}

func (conn *connUDP) SetReadDeadline(timeout time.Time) error {
	return conn.connection.SetReadDeadline(timeout)
}

func (conn *connUDP) ReadFromSessionUDP(m []byte) (int, *SessionUDPData, error) {
	return ReadFromSessionUDP(conn.connection, m)
}

func (conn *connUDP) Close() error {
	conn.finishWrite()
	return conn.connection.Close()
}

func (conn *connUDP) writeHandler(srv *Server) bool {
	return conn.writeHandlerWithFunc(srv, func(srv *Server, wreq writeReq) error {
		data := wreq.data()
		wreqUDP := wreq.(*writeReqUDP)
		writeTimeout := srv.writeTimeout()
		buf := &bytes.Buffer{}
		err := data.MarshalBinary(buf)
		if err != nil {
			return err
		}

		var msg []byte
		ns := conn.ns
		if ns.Handshakes < 2 {
			//log.Printf("handshake encrypting %d bytes with %p: %v", len(buf.Bytes()), ns.Hs, buf.Bytes())
			res, cs0, cs1, err := ns.Hs.WriteMessage(nil, buf.Bytes())
			if err != nil {
				return err
			}

			ns.Cs0 = cs0
			ns.Cs1 = cs1

			msg = res
			//log.Printf("handshake encrypted %d bytes with %p: %v", len(msg), ns.Hs, msg)
			//log.Printf("handshake encrypted %d->%d bytes with %p", len(buf.Bytes()), len(msg), ns.Hs)
			ns.Handshakes++
		} else {
			//log.Printf("encrypting %d bytes with %p: %v", len(buf.Bytes()), ns.Hs, buf.Bytes())
			var cs *noise.CipherState
			if (conn.ns.Initiator) {
				cs = ns.Cs0
			} else {
				cs = ns.Cs1
			}
			res := cs.Encrypt(nil, nil, buf.Bytes())
			msg = res
			//log.Printf("encrypted %d bytes with %p: %v", len(msg), ns.Hs, msg)
			//log.Printf("encrypted %d->%d bytes with %p", len(buf.Bytes()), len(msg), ns.Hs)
		}

		conn.connection.SetWriteDeadline(time.Now().Add(writeTimeout))
		_, err = WriteToSessionUDP(conn.connection, msg, wreqUDP.sessionData)
		return err
	})
}

func newConnectionTCP(c net.Conn, srv *Server) Conn {
	connection := &connTCP{connBase: connBase{writeChan: make(chan writeReq, 10000), closeChan: make(chan bool), finChan: make(chan bool), closed: 0}, connection: c}
	go writeToConnection(connection, srv)
	return connection
}

type RandomInc byte

// FIXME: we probably need a better RNG than this... :P
func (r *RandomInc) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(*r)
		*r = (*r) + 1
	}
	return len(p), nil
}

func setupNoise(c *net.UDPConn, initiator bool) {
}

func newConnectionUDP(c *net.UDPConn, srv *Server, initiator bool) Conn {

	connection := &connUDP{connBase: connBase{writeChan: make(chan writeReq, 10000), closeChan: make(chan bool), finChan: make(chan bool), closed: 0}, connection: c}

	// log.Printf("newConnectionUDP called with initiator=%v and conn=%p", initiator, connection)
	// debug.PrintStack()

	go writeToConnection(connection, srv)
	return connection
}

func writeToConnection(conn connWriter, srv *Server) {
	for conn.writeHandler(srv) {
	}
	for conn.writeEndHandler(srv.syncTimeout()) {
	}
	conn.sendFinish(srv.syncTimeout())
}
