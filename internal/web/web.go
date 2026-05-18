package web

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/user/pntgw/internal/config"
)

//go:embed ui/*
var uiFS embed.FS

// Snapshot is the live status surfaced to the web UI.
type Snapshot struct {
	HW         string  `json:"hw"`
	SW         string  `json:"sw"`
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	AltM       float64 `json:"alt_m"`
	UncM       float64 `json:"unc_m"`
	Sats       int     `json:"sats"`
	Valid      bool    `json:"valid"`
	LastPollMs int64   `json:"last_poll_ms"`
	LastNMEA   string  `json:"last_nmea"`
	TCPClients int     `json:"tcp_clients"`
	UDPDests   int     `json:"udp_dests"`
	PNTSource  string  `json:"pnt_source"` // "gps" | "starshield" | "unknown"
}

type Server struct {
	cfg       *config.Store
	snap      func() Snapshot
	notify    chan struct{}
	setSource func(starshield bool) error

	mu     sync.Mutex
	server *http.Server
}

// New returns a Server that reads from cfg and calls snap() for live status.
// notify is signalled (non-blocking) whenever config is updated through the API.
// setSource issues the dish PNT-source switch; it is only ever called from an
// explicit user action on /api/pnt-source.
func New(cfg *config.Store, snap func() Snapshot, notify chan struct{}, setSource func(starshield bool) error) *Server {
	return &Server{cfg: cfg, snap: snap, notify: notify, setSource: setSource}
}

func (s *Server) Start(bind string) error {
	s.mu.Lock()
	if s.server != nil {
		s.server.Close()
		s.server = nil
	}
	s.mu.Unlock()

	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/pnt-source", s.handlePNTSource)
	mux.Handle("/", http.FileServer(http.FS(sub)))

	srv := &http.Server{
		Addr:              bind,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.mu.Lock()
	s.server = srv
	s.mu.Unlock()

	log.Printf("web: listening on %s", bind)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("web: %v", err)
		}
	}()
	return nil
}

func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.server.Shutdown(ctx)
		s.server = nil
	}
}

// handlePNTSource: POST {"source":"gps"|"starshield"} flips the dish PNT
// source. No persistence — the dish holds runtime state, reboot clears it,
// pntgw never re-asserts.
func (s *Server) handlePNTSource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Source string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var starshield bool
	switch body.Source {
	case "starshield":
		starshield = true
	case "gps":
		starshield = false
	default:
		http.Error(w, `source must be "gps" or "starshield"`, http.StatusBadRequest)
		return
	}
	if s.setSource == nil {
		http.Error(w, "pnt source switching unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.setSource(starshield); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true,"source":"` + body.Source + `"}`))
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(s.snap())
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(s.cfg.Get())
	case http.MethodPut:
		var body config.Config
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.cfg.Update(func(c *config.Config) {
			if body.DishAddr != "" {
				c.DishAddr = body.DishAddr
			}
			if body.PollIntervalMs > 0 {
				c.PollIntervalMs = body.PollIntervalMs
			}
			c.TCP = body.TCP
			c.UDPDestinations = body.UDPDestinations
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		select {
		case s.notify <- struct{}{}:
		default:
		}
		_ = json.NewEncoder(w).Encode(s.cfg.Get())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
