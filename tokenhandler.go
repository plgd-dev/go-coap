package coap

import "sync"

func handleBySessionTokenHandler(w ResponseWriter, r *Request, next HandlerFunc) {
	r.Client.networkSession.TokenHandler().Handle(w, r, next)
}

//TokenHandler container for regirstration handlers by token
type TokenHandler struct {
	tokenHandlers     map[[MaxTokenSize]byte]HandlerFunc
	tokenHandlersLock sync.Mutex
}

//Handle call handler for request if exist otherwise use next
func (s *TokenHandler) Handle(w ResponseWriter, r *Request, next HandlerFunc) {
	//validate token
	var token [MaxTokenSize]byte
	copy(token[:], r.Msg.Token())
	s.tokenHandlersLock.Lock()
	h := s.tokenHandlers[token]
	s.tokenHandlersLock.Unlock()
	if h != nil {
		h(w, r)
		return
	}
	if next != nil {
		next(w, r)
	}
}

//Add register handler for token
func (s *TokenHandler) Add(token []byte, handler func(w ResponseWriter, r *Request)) error {
	var t [MaxTokenSize]byte
	copy(t[:], token)
	s.tokenHandlersLock.Lock()
	defer s.tokenHandlersLock.Unlock()
	if s.tokenHandlers[t] != nil {
		return ErrTokenAlreadyExist
	}
	s.tokenHandlers[t] = handler
	return nil
}

//Remove unregister handler for token
func (s *TokenHandler) Remove(token []byte) error {
	var t [MaxTokenSize]byte
	copy(t[:], token)
	s.tokenHandlersLock.Lock()
	defer s.tokenHandlersLock.Unlock()
	if s.tokenHandlers[t] == nil {
		return ErrTokenNotExist
	}
	delete(s.tokenHandlers, t)
	return nil
}
