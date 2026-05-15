package tcpsrv

import (
	"context"
	"errors"
	"log"
	"net"
	"sync"
	"time"
)

// Server fans an NMEA stream out to every connected TCP client.
type Server struct {
	mu       sync.RWMutex
	bind     string
	listener net.Listener
	clients  map[net.Conn]struct{}
	stop     chan struct{}
}

func New() *Server {
	return &Server{
		clients: map[net.Conn]struct{}{},
		stop:    make(chan struct{}),
	}
}

// Start (re)starts the listener on bind. Safe to call repeatedly; will close
// the previous listener and reopen on a new address.
func (s *Server) Start(bind string) error {
	s.mu.Lock()
	if s.listener != nil && s.bind == bind {
		s.mu.Unlock()
		return nil
	}
	if s.listener != nil {
		s.listener.Close()
		s.listener = nil
	}
	s.mu.Unlock()

	ln, err := net.Listen("tcp", bind)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.bind = bind
	s.listener = ln
	s.mu.Unlock()

	go s.acceptLoop(ln)
	log.Printf("tcpsrv: listening on %s", bind)
	return nil
}

// Stop closes the listener and disconnects all clients.
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		s.listener.Close()
		s.listener = nil
	}
	for c := range s.clients {
		c.Close()
		delete(s.clients, c)
	}
}

func (s *Server) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("tcpsrv: accept: %v", err)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		s.mu.Lock()
		// Make sure this listener is still the current one
		if s.listener != ln {
			conn.Close()
			s.mu.Unlock()
			return
		}
		s.clients[conn] = struct{}{}
		s.mu.Unlock()
		log.Printf("tcpsrv: client connected: %s", conn.RemoteAddr())
	}
}

// Broadcast writes the given data to every connected client. A client that
// fails to receive is dropped.
func (s *Server) Broadcast(ctx context.Context, data []byte) {
	s.mu.RLock()
	conns := make([]net.Conn, 0, len(s.clients))
	for c := range s.clients {
		conns = append(conns, c)
	}
	s.mu.RUnlock()

	for _, c := range conns {
		_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if _, err := c.Write(data); err != nil {
			log.Printf("tcpsrv: client %s drop: %v", c.RemoteAddr(), err)
			s.mu.Lock()
			delete(s.clients, c)
			s.mu.Unlock()
			c.Close()
		}
	}
}

// ClientCount returns the number of currently connected TCP clients.
func (s *Server) ClientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}
