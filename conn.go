package coap

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

/*

 */

type ConnTCP struct {
	heartBeat  time.Duration
	connection net.Conn // i/o connection if TCP was used
	lock       sync.Mutex
}

func (c *ConnTCP) WriteContext(ctx context.Context, buffer []byte) error {
	written := 0
	c.lock.Lock()
	defer c.lock.Unlock()
	for written < len(buffer) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		err := c.connection.SetWriteDeadline(time.Now().Add(waitTimer))
		if err != nil {
			return fmt.Errorf("cannot set write deadline for tcp connection: %v", err)
		}
		n, err := c.connection.Write(buffer[written:])
		if err != nil {
			if passError(err) {
				continue
			}
			return fmt.Errorf("cannot write to tcp connection")
		}
		written += n
	}
	return nil
}

func (c *ConnTCP) LocalAddr() net.Addr {
	return c.connection.LocalAddr()
}

func (c *ConnTCP) RemoteAddr() net.Addr {
	return c.connection.RemoteAddr()
}

func (c *ConnTCP) Close() error {
	return c.connection.Close()
}

type ConnUDP struct {
	heartBeat  time.Duration
	connection *net.UDPConn // i/o connection if UDP was used
	lock       sync.Mutex
}

func (c *ConnUDP) LocalAddr() net.Addr {
	return c.connection.LocalAddr()
}

func (c *ConnUDP) RemoteAddr() net.Addr {
	return c.connection.RemoteAddr()
}

func (c *ConnUDP) Close() error {
	return c.connection.Close()
}

func (c *ConnUDP) WriteContext(ctx context.Context, sessionUDPData *SessionUDPData, buffer []byte) error {
	written := 0
	c.lock.Lock()
	defer c.lock.Unlock()
	for written < len(buffer) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		err := c.connection.SetWriteDeadline(time.Now().Add(c.heartBeat))
		if err != nil {
			return fmt.Errorf("cannot set write deadline for tcp connection: %v", err)
		}
		n, err := WriteToSessionUDP(c.connection, sessionUDPData, buffer[written:])
		if err != nil {
			if passError(err) {
				continue
			}
			return fmt.Errorf("cannot write to tcp connection")
		}
		written += n
	}

	return nil
}

func (c *ConnUDP) ReadContext(ctx context.Context, buffer []byte) (int, *SessionUDPData, error) {
	for {
		select {
		case <-ctx.Done():
			if ctx.Err() != nil {
				return -1, nil, fmt.Errorf("cannot read from udp connection: %v", ctx.Err())
			}
			return -1, nil, fmt.Errorf("cannot read from udp connection")
		default:
		}

		err := c.connection.SetReadDeadline(time.Now().Add(c.heartBeat))
		if err != nil {
			return -1, nil, fmt.Errorf("cannot set read deadline for udp connection: %v", err)
		}
		n, s, err := ReadFromSessionUDP(c.connection, buffer)
		if err != nil {
			if passError(err) {
				continue
			}
			return -1, nil, fmt.Errorf("cannot read from udp connection: %v", ctx.Err())
		}
		return n, s, err
	}
}

func NewConnTCP(c net.Conn, heartBeat time.Duration) *ConnTCP {
	connection := ConnTCP{connection: c, heartBeat: heartBeat}
	return &connection
}

func NewConnUDP(c *net.UDPConn, heartBeat time.Duration) *ConnUDP {
	connection := ConnUDP{connection: c, heartBeat: heartBeat}
	return &connection
}
