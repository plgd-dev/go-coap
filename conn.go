package coap

import (
	"net"
	"sync/atomic"
	"time"
)

type writeReq interface {
}

type writeReqBase struct {
	req      Message
	respChan chan error
}

type writeReqTCP struct {
	writeReqBase
}

type writeReqUDP struct {
	writeReqBase
	sessionData *SessionUDPData
}

type conn interface {
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
	Write(w writeReq) error
	Close() error
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

func (conn *connTCP) Write(w writeReq) error {
	if atomic.LoadInt32(&conn.closed) > 0 {
		return ErrConnectionClosed
	}
	conn.writeChan <- w
	err := <-w.(*writeReqTCP).respChan
	return err
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

func (conn *connUDP) Write(w writeReq) error {
	if atomic.LoadInt32(&conn.closed) > 0 {
		return ErrConnectionClosed
	}
	conn.writeChan <- w
	return <-w.(*writeReqUDP).respChan
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

func newConnectionTCP(c net.Conn, srv *Server) conn {
	connection := &connTCP{connBase: connBase{writeChan: make(chan writeReq), closeChan: make(chan bool), finChan: make(chan bool), closed: 0}, connection: c}
	go writeTCP(connection, srv)
	return connection
}

func newConnectionUDP(c *net.UDPConn, srv *Server) conn {
	connection := &connUDP{connBase: connBase{writeChan: make(chan writeReq), closeChan: make(chan bool), finChan: make(chan bool), closed: 0}, connection: c}
	go writeUDP(connection, srv)
	return connection
}

func writeTCP(conn *connTCP, srv *Server) {
	wr := srv.acquireWriter(conn.connection)
LOOP:
	for {
		select {
		case wreq := <-conn.writeChan:
			{
				wreqTCP := wreq.(*writeReqTCP)
				data, err := wreqTCP.req.MarshalBinary()
				if err != nil {
					wreqTCP.respChan <- err
					continue
				}
				writeTimeout := srv.getWriteTimeout()
				conn.connection.SetWriteDeadline(time.Now().Add(writeTimeout))
				dataLen := len(data)
				dataWritten := 0
				for dataLen > 0 {
					n, err1 := conn.connection.Write(data[dataWritten : dataLen-dataWritten])
					if err1 != nil {
						err = err1
						break
					}
					dataLen -= n
					dataWritten += n
					wr.Flush()
				}
				wreqTCP.respChan <- err
			}
		case <-conn.closeChan:
			break LOOP
		}
	}

END_LOOP:
	for {
		select {
		case wreq := <-conn.writeChan:
			wreqTCP := wreq.(*writeReqTCP)
			wreqTCP.respChan <- ErrConnectionClosed
		default:
			break END_LOOP
		}
	}

	srv.releaseWriter(wr)
	conn.finChan <- true
}

func writeUDP(conn *connUDP, srv *Server) {
LOOP:
	for {
		select {
		case wreq := <-conn.writeChan:
			wreqUDP := wreq.(*writeReqUDP)
			data, err := wreqUDP.req.MarshalBinary()
			if err != nil {
				wreqUDP.respChan <- err
				continue
			}
			writeTimeout := srv.getWriteTimeout()
			conn.connection.SetWriteDeadline(time.Now().Add(writeTimeout))
			dataLen := len(data)
			dataWritten := 0
			for dataLen > 0 {
				n, err1 := WriteToSessionUDP(conn.connection, data[dataWritten:dataLen-dataWritten], wreqUDP.sessionData)
				if err1 != nil {
					err = err1
					break
				}
				dataLen -= n
				dataWritten += n

			}
			wreqUDP.respChan <- err
		case <-conn.closeChan:
			break LOOP
		}
	}

END_LOOP:
	for {
		select {
		case wreq := <-conn.writeChan:
			wreqUDP := wreq.(*writeReqUDP)
			wreqUDP.respChan <- ErrConnectionClosed
		default:
			break END_LOOP
		}
	}

	conn.finChan <- true
}
