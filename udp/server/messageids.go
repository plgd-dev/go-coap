package server

import (
	"container/list"
	"errors"
	"fmt"
	"time"

	"github.com/plgd-dev/go-coap/v3/pkg/sync/atomic"
	"github.com/plgd-dev/go-coap/v3/udp/client"
)

// ErrEndpointMessageIDsFull means that no endpoint sequence can be safely evicted.
var ErrEndpointMessageIDsFull = errors.New("endpoint message ID cache is full")

type endpointMessageID struct {
	endpoint string
	counter  *atomic.Uint32
	element  *list.Element
	users    int
	expires  time.Time
}

// Access is serialized by Server.connsMutex.
type endpointMessageIDs struct {
	limit     uint32
	unlimited bool
	entries   map[string]*endpointMessageID
	byOrder   map[*list.Element]*endpointMessageID
	order     list.List
}

func newEndpointMessageIDs(limit uint32, unlimited bool) *endpointMessageIDs {
	return &endpointMessageIDs{
		limit: limit, unlimited: unlimited, entries: make(map[string]*endpointMessageID),
		byOrder: make(map[*list.Element]*endpointMessageID),
	}
}

func (m *endpointMessageIDs) acquire(endpoint string, initial func() uint32, now time.Time) (*atomic.Uint32, error) {
	if entry := m.entries[endpoint]; entry != nil {
		if entry.element != nil {
			delete(m.byOrder, entry.element)
			m.order.Remove(entry.element)
			entry.element = nil
		}
		entry.users++
		return entry.counter, nil
	}
	if !m.unlimited && uint64(len(m.entries)) >= uint64(m.limit) {
		oldest := m.order.Front()
		if oldest == nil || now.Before(m.byOrder[oldest].expires) {
			return nil, ErrEndpointMessageIDsFull
		}
		entry := m.byOrder[oldest]
		delete(m.entries, entry.endpoint)
		delete(m.byOrder, oldest)
		m.order.Remove(oldest)
	}
	counter := atomic.NewUint32(initial())
	entry := &endpointMessageID{endpoint: endpoint, counter: counter, users: 1}
	m.entries[endpoint] = entry
	return counter, nil
}

func (m *endpointMessageIDs) release(endpoint string, now time.Time) error {
	entry := m.entries[endpoint]
	if entry == nil || entry.users <= 0 {
		return fmt.Errorf("cannot release endpoint message ID sequence %q: not acquired", endpoint)
	}
	entry.users--
	if entry.users == 0 {
		entry.expires = now.Add(client.ExchangeLifetime)
		entry.element = m.order.PushBack(nil)
		m.byOrder[entry.element] = entry
	}
	return nil
}
