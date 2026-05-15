package config

import (
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

type TCP struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Bind    string `yaml:"bind" json:"bind"`
}

type Web struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Bind    string `yaml:"bind" json:"bind"`
}

type Config struct {
	DishAddr        string   `yaml:"dish_addr" json:"dish_addr"`
	PollIntervalMs  int      `yaml:"poll_interval_ms" json:"poll_interval_ms"`
	TCP             TCP      `yaml:"tcp" json:"tcp"`
	UDPDestinations []string `yaml:"udp_destinations" json:"udp_destinations"`
	Web             Web      `yaml:"web" json:"web"`
}

func Default() *Config {
	return &Config{
		DishAddr:        "192.168.100.1:9200",
		PollIntervalMs:  1000,
		TCP:             TCP{Enabled: true, Bind: "0.0.0.0:10110"},
		UDPDestinations: []string{},
		Web:             Web{Enabled: true, Bind: "0.0.0.0:8080"},
	}
}

type Store struct {
	mu   sync.RWMutex
	path string
	cfg  *Config
}

func New(path string) (*Store, error) {
	s := &Store{path: path}
	if err := s.load(); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		s.cfg = Default()
		if err := s.save(); err != nil {
			return nil, fmt.Errorf("write default: %w", err)
		}
	}
	return s, nil
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	c := Default()
	if err := yaml.Unmarshal(b, c); err != nil {
		return fmt.Errorf("yaml: %w", err)
	}
	s.cfg = c
	return nil
}

func (s *Store) save() error {
	b, err := yaml.Marshal(s.cfg)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) Get() *Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := *s.cfg
	c.UDPDestinations = append([]string(nil), s.cfg.UDPDestinations...)
	return &c
}

func (s *Store) Update(fn func(*Config)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.cfg)
	return s.save()
}
