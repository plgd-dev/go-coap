package client

import (
	"sync"

	"github.com/plgd-dev/go-coap/v3/message/pool"
	"go.uber.org/atomic"
)

type ReceivedMessageReaderClient interface {
	Done() <-chan struct{}
	ProcessReceivedMessage(req *pool.Message)
}

type ReceivedMessageReader[C ReceivedMessageReaderClient] struct {
	queue chan *pool.Message
	cc    C

	private struct {
		mutex           sync.Mutex
		loopDone        chan struct{}
		readingMessages *atomic.Bool
	}
}

// NewReceivedMessageReader creates a new ReceivedMessageReader[C] instance.
func NewReceivedMessageReader[C ReceivedMessageReaderClient](cc C, queueSize int) *ReceivedMessageReader[C] {
	r := ReceivedMessageReader[C]{
		queue: make(chan *pool.Message, queueSize),
		cc:    cc,
		private: struct {
			mutex           sync.Mutex
			loopDone        chan struct{}
			readingMessages *atomic.Bool
		}{
			loopDone:        make(chan struct{}),
			readingMessages: atomic.NewBool(true),
		},
	}

	go r.loop(r.private.loopDone, r.private.readingMessages)
	return &r
}

// C returns the channel to push received messages to.
func (r *ReceivedMessageReader[C]) C() chan<- *pool.Message {
	return r.queue
}

func (r *ReceivedMessageReader[C]) loop(loopDone chan struct{}, readingMessages *atomic.Bool) {
	for {
		select {
		// if the loop is replaced, the old loop will be closed
		case <-loopDone:
			return
		// process received message until the queue is empty
		case req := <-r.queue:
			readingMessages.Store(false)
			r.cc.ProcessReceivedMessage(req)
			r.private.mutex.Lock()
			readingMessages.Store(true)
			r.private.mutex.Unlock()
		// if the client is closed, the loop will be closed
		case <-r.cc.Done():
			return
		}
	}
}

// TryToReplaceLoop replace the loop with a new one if the loop is not reading messages.
func (r *ReceivedMessageReader[C]) TryToReplaceLoop() {
	r.private.mutex.Lock()
	if r.private.readingMessages.Load() {
		r.private.mutex.Unlock()
		return
	}
	defer r.private.mutex.Unlock()
	close(r.private.loopDone)
	loopDone := make(chan struct{})
	readingMessages := atomic.NewBool(true)
	r.private.loopDone = loopDone
	r.private.readingMessages = readingMessages
	go r.loop(loopDone, readingMessages)
}
