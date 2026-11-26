package server

import (
	"container/list"

	"github.com/plgd-dev/go-coap/v3/pkg/sync/atomic"
)

const maxEndpointMessageIDs = 4096

type endpointMessageID struct {
	endpoint string
	counter  *atomic.Uint32
	element  *list.Element
}

// Access is serialized by Server.connsMutex.
type endpointMessageIDs struct {
	limit   int
	entries map[string]*endpointMessageID
	byOrder map[*list.Element]*endpointMessageID
	order   list.List
}

func newEndpointMessageIDs(limit int) *endpointMessageIDs {
	return &endpointMessageIDs{
		limit: limit, entries: make(map[string]*endpointMessageID),
		byOrder: make(map[*list.Element]*endpointMessageID),
	}
}

func (m *endpointMessageIDs) get(endpoint string, initial func() uint32) *atomic.Uint32 {
	if entry := m.entries[endpoint]; entry != nil {
		m.order.MoveToFront(entry.element)
		return entry.counter
	}
	if m.order.Len() == m.limit {
		oldest := m.order.Back()
		delete(m.entries, m.byOrder[oldest].endpoint)
		delete(m.byOrder, oldest)
		m.order.Remove(oldest)
	}
	counter := atomic.NewUint32(initial())
	entry := &endpointMessageID{endpoint: endpoint, counter: counter}
	entry.element = m.order.PushFront(nil)
	m.entries[endpoint] = entry
	m.byOrder[entry.element] = entry
	return counter
}
