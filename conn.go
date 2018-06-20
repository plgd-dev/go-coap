package coap

import (
	"log"
	"net"
	"sync/atomic"
	"time"
)

type writeReq interface {
	sendResp(err error, timeout time.Duration)
	marshalBinary() ([]byte, error)
	waitResp(timeout time.Duration) error
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

func (wreq *writeReqBase) marshalBinary() ([]byte, error) {
	return wreq.req.MarshalBinary()
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
		data, err := wreq.marshalBinary()
		if err != nil {
			return err
		}
		wr := srv.acquireWriter(conn.connection)
		defer srv.releaseWriter(wr)
		writeTimeout := srv.writeTimeout()
		conn.connection.SetWriteDeadline(time.Now().Add(writeTimeout))
		dataLen := len(data)
		dataWritten := 0
		for dataLen > 0 {
			n, err := wr.Write(data[dataWritten : dataLen-dataWritten])
			if err != nil {
				return err
			}
			dataLen -= n
			dataWritten += n
			wr.Flush()
		}
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
		data, err := wreq.marshalBinary()
		if err != nil {
			return err
		}
		wreqUDP := wreq.(*writeReqUDP)
		writeTimeout := srv.writeTimeout()
		conn.connection.SetWriteDeadline(time.Now().Add(writeTimeout))
		dataLen := len(data)
		dataWritten := 0
		for dataLen > 0 {
			n, err := WriteToSessionUDP(conn.connection, data[dataWritten:dataLen-dataWritten], wreqUDP.sessionData)
			if err != nil {
				return err
			}
			dataLen -= n
			dataWritten += n
		}
		return nil
	})
}

func newConnectionTCP(c net.Conn, srv *Server) Conn {
	connection := &connTCP{connBase: connBase{writeChan: make(chan writeReq, 10000), closeChan: make(chan bool), finChan: make(chan bool), closed: 0}, connection: c}
	go writeToConnection(connection, srv)
	return connection
}

func newConnectionUDP(c *net.UDPConn, srv *Server) Conn {
	connection := &connUDP{connBase: connBase{writeChan: make(chan writeReq, 10000), closeChan: make(chan bool), finChan: make(chan bool), closed: 0}, connection: c}
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
