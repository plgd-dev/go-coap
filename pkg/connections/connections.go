package connections

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/plgd-dev/go-coap/v3/pkg/sync/atomic"
)

type Connections struct {
	data *sync.Map
}

func New() *Connections {
	return &Connections{
		data: &sync.Map{},
	}
}

type Connection interface {
	Context() context.Context
	CheckExpirations(now time.Time)
	Close() error
	RemoteAddr() net.Addr
}

type monitored struct {
	Connection
	checking atomic.Bool
}

func (c *Connections) Store(conn Connection) {
	c.data.Store(conn.RemoteAddr().String(), &monitored{Connection: conn})
}

func (c *Connections) length() int {
	var l int
	c.data.Range(func(_, _ interface{}) bool {
		l++
		return true
	})
	return l
}

func (c *Connections) copyConnections() []*monitored {
	m := make([]*monitored, 0, c.length())
	c.data.Range(func(_, value interface{}) bool {
		con, ok := value.(*monitored)
		if !ok {
			panic(fmt.Errorf("invalid type %T in connections map", con))
		}
		m = append(m, con)
		return true
	})
	return m
}

// CheckExpirations schedules each connection's check independently, so a check
// blocked on its peer cannot prevent checks for other connections.
func (c *Connections) CheckExpirations(now time.Time) {
	for _, cc := range c.copyConnections() {
		select {
		case <-cc.Context().Done():
			continue
		default:
		}
		if !cc.checking.CompareAndSwap(false, true) {
			continue
		}
		go func(cc *monitored) {
			defer cc.checking.Store(false)
			cc.CheckExpirations(now)
		}(cc)
	}
}

func (c *Connections) Close() {
	for _, cc := range c.copyConnections() {
		_ = cc.Close()
	}
}

func (c *Connections) Delete(conn Connection) {
	c.data.Delete(conn.RemoteAddr().String())
}
