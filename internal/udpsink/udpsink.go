package udpsink

import (
	"log"
	"net"
	"sync"
	"time"
)

// Sink sends UDP datagrams to a configurable list of destinations.
// SetDestinations is safe to call at any time.
type Sink struct {
	mu    sync.RWMutex
	dests []*resolvedDest
	conn  *net.UDPConn
}

type resolvedDest struct {
	raw  string
	addr *net.UDPAddr
}

func New() (*Sink, error) {
	c, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		return nil, err
	}
	return &Sink{conn: c}, nil
}

// SetDestinations replaces the destination list. Bad entries are logged and skipped.
func (s *Sink) SetDestinations(addrs []string) {
	resolved := make([]*resolvedDest, 0, len(addrs))
	for _, a := range addrs {
		ua, err := net.ResolveUDPAddr("udp", a)
		if err != nil {
			log.Printf("udpsink: bad destination %q: %v", a, err)
			continue
		}
		resolved = append(resolved, &resolvedDest{raw: a, addr: ua})
	}
	s.mu.Lock()
	s.dests = resolved
	s.mu.Unlock()
}

// Send transmits data to every configured destination. Errors are logged but
// do not stop the loop — UDP is best-effort.
func (s *Sink) Send(data []byte) {
	s.mu.RLock()
	dests := s.dests
	s.mu.RUnlock()
	if len(dests) == 0 {
		return
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
	for _, d := range dests {
		if _, err := s.conn.WriteToUDP(data, d.addr); err != nil {
			log.Printf("udpsink: send to %s: %v", d.raw, err)
		}
	}
}

// DestCount returns the number of configured destinations.
func (s *Sink) DestCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.dests)
}

// Close releases the socket.
func (s *Sink) Close() error {
	return s.conn.Close()
}
