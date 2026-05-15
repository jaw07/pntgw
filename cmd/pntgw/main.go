package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/user/pntgw/internal/config"
	"github.com/user/pntgw/internal/dish"
	"github.com/user/pntgw/internal/nmea"
	"github.com/user/pntgw/internal/tcpsrv"
	"github.com/user/pntgw/internal/udpsink"
	"github.com/user/pntgw/internal/web"
)

func main() {
	cfgPath := flag.String("config", "/config/pntgw.yaml", "path to YAML config")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	cfg, err := config.New(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	tcp := tcpsrv.New()
	udp, err := udpsink.New()
	if err != nil {
		log.Fatalf("udpsink: %v", err)
	}
	defer udp.Close()

	// State shared with the web layer
	var (
		stateMu  sync.RWMutex
		lastPNT  *dish.PNT
		lastNMEA string
	)

	snap := func() web.Snapshot {
		stateMu.RLock()
		p := lastPNT
		nm := lastNMEA
		stateMu.RUnlock()
		s := web.Snapshot{
			LastNMEA:   nm,
			TCPClients: tcp.ClientCount(),
			UDPDests:   udp.DestCount(),
		}
		if p != nil {
			s.HW = p.HardwareVersion
			s.SW = p.SoftwareVersion
			s.Lat = p.Lat
			s.Lon = p.Lon
			s.AltM = p.AltMeters
			s.UncM = p.UncertaintyM
			s.Sats = p.Sats
			s.Valid = p.Valid
			s.LastPollMs = p.Time.UnixMilli()
		}
		return s
	}

	cfgChanged := make(chan struct{}, 1)
	w := web.New(cfg, snap, cfgChanged)

	// Apply config to sinks/listeners. Re-applies safely on change.
	apply := func(c *config.Config) {
		udp.SetDestinations(c.UDPDestinations)
		if c.TCP.Enabled {
			if err := tcp.Start(c.TCP.Bind); err != nil {
				log.Printf("tcp start: %v", err)
			}
		} else {
			tcp.Stop()
		}
		if c.Web.Enabled {
			if err := w.Start(c.Web.Bind); err != nil {
				log.Printf("web start: %v", err)
			}
		} else {
			w.Stop()
		}
	}

	apply(cfg.Get())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// Polling loop: connects to the dish, reconnects on failure, polls at the
	// configured interval, fans out NMEA over TCP+UDP.
	go pollLoop(ctx, cfg, tcp, udp, &stateMu, &lastPNT, &lastNMEA)

	// Config-change loop: re-apply config when the web UI signals.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-cfgChanged:
				apply(cfg.Get())
			}
		}
	}()

	sig := <-sigCh
	log.Printf("got signal %s, shutting down", sig)
	cancel()
	tcp.Stop()
	w.Stop()
}

func pollLoop(
	ctx context.Context,
	cfg *config.Store,
	tcp *tcpsrv.Server,
	udp *udpsink.Sink,
	mu *sync.RWMutex,
	lastPNT **dish.PNT,
	lastNMEA *string,
) {
	var (
		client     *dish.Client
		clientAddr string
	)
	defer func() {
		if client != nil {
			client.Close()
		}
	}()

	// retryWait is a small backoff applied only on dial failures.
	var retryWait atomic.Int64
	retryWait.Store(int64(time.Second))

	for {
		c := cfg.Get()
		interval := time.Duration(c.PollIntervalMs) * time.Millisecond
		if interval < 100*time.Millisecond {
			interval = 100 * time.Millisecond
		}

		// (Re)dial if needed
		if client == nil || clientAddr != c.DishAddr {
			if client != nil {
				client.Close()
				client = nil
			}
			dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			nc, err := dish.Dial(dialCtx, c.DishAddr)
			cancel()
			if err != nil {
				log.Printf("dish dial %s: %v", c.DishAddr, err)
				if sleep(ctx, time.Duration(retryWait.Load())) {
					return
				}
				continue
			}
			client = nc
			clientAddr = c.DishAddr
			log.Printf("dish: connected to %s", clientAddr)
		}

		pollCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		pnt, err := client.Poll(pollCtx)
		cancel()
		if err != nil {
			log.Printf("dish poll: %v", err)
			client.Close()
			client = nil
			if sleep(ctx, time.Second) {
				return
			}
			continue
		}

		fix := nmea.Fix{
			Time:         pnt.Time,
			Valid:        pnt.Valid,
			Lat:          pnt.Lat,
			Lon:          pnt.Lon,
			AltMeters:    pnt.AltMeters,
			UncertaintyM: pnt.UncertaintyM,
			Sats:         pnt.Sats,
		}
		sentences := nmea.SentencesString(fix)
		data := []byte(sentences)

		tcp.Broadcast(ctx, data)
		udp.Send(data)

		mu.Lock()
		*lastPNT = pnt
		*lastNMEA = sentences
		mu.Unlock()

		if sleep(ctx, interval) {
			return
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}
