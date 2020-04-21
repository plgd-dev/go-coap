package tcp

import (
	"fmt"
	"sync"

	"github.com/go-ocf/go-coap/v2/message"
)

//TokenHandler container for regirstration handlers by token
type TokenHandler struct {
	handlers     map[[message.MaxTokenSize]byte]HandlerFunc
	handlersLock sync.Mutex
}

func NewTokenHandler() *TokenHandler {
	return &TokenHandler{
		handlers: make(map[[8]byte]HandlerFunc),
	}
}

//Handle call handler for request if exist otherwise use next
func (s *TokenHandler) Handle(w *ResponseWriter, r *Request, next HandlerFunc) {
	var t [message.MaxTokenSize]byte
	copy(t[:], r.Token())
	//validate token
	s.handlersLock.Lock()
	h := s.handlers[t]
	s.handlersLock.Unlock()
	if h != nil {
		h(w, r)
		return
	}
	if next != nil {
		next(w, r)
	}
}

//Add register handler for token
func (s *TokenHandler) Add(token []byte, handler HandlerFunc) error {
	var t [message.MaxTokenSize]byte
	copy(t[:], token)
	s.handlersLock.Lock()
	defer s.handlersLock.Unlock()
	if s.handlers[t] != nil {
		return fmt.Errorf("token already exist")
	}
	s.handlers[t] = handler
	return nil
}

//Remove unregister handler for token
func (s *TokenHandler) Remove(token []byte) error {
	var t [message.MaxTokenSize]byte
	copy(t[:], token)
	s.handlersLock.Lock()
	defer s.handlersLock.Unlock()
	if s.handlers[t] == nil {
		return fmt.Errorf("token not exist")
	}
	delete(s.handlers, t)
	return nil
}
