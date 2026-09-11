package maxmindgeo

import (
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/Jstarzz/geotagger/internal/geo"

	"github.com/oschwald/maxminddb-golang/v2"
)

type Result struct {
	Country     string
	CountryCode string
}

type Lookup interface {
	Lookup(netip.Addr) (Result, bool, error)
	Version() string
	Close() error
}

type MaxMind struct {
	mu      sync.RWMutex
	path    string
	reader  *maxminddb.Reader
	version string
	modTime time.Time
}

func OpenMaxMind(path string) (*MaxMind, error) {
	m := &MaxMind{path: path}
	if err := m.reload(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *MaxMind) Lookup(ip netip.Addr) (geo.Result, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var record struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
			Names   struct {
				English string `maxminddb:"en"`
			} `maxminddb:"names"`
		} `maxminddb:"country"`
	}
	if err := m.reader.Lookup(ip).Decode(&record); err != nil {
		return geo.Result{}, false, err
	}
	if record.Country.ISOCode == "" && record.Country.Names.English == "" {
		return geo.Result{}, false, nil
	}
	return geo.Result{Country: record.Country.Names.English, CountryCode: record.Country.ISOCode}, true, nil
}

func (m *MaxMind) Version() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.version
}

func (m *MaxMind) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reader == nil {
		return nil
	}
	err := m.reader.Close()
	m.reader = nil
	return err
}

func (m *MaxMind) Watch(interval time.Duration, stop <-chan struct{}, onError func(error), onReload func(string)) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			changed, err := m.ReloadIfChanged()
			if err != nil && onError != nil {
				onError(err)
			}
			if changed && onReload != nil {
				onReload(m.Version())
			}
		case <-stop:
			return
		}
	}
}

func (m *MaxMind) ReloadIfChanged() (bool, error) {
	info, err := statFile(m.path)
	if err != nil {
		return false, err
	}
	m.mu.RLock()
	same := !info.ModTime().After(m.modTime) && info.Size() > 0
	m.mu.RUnlock()
	if same {
		return false, nil
	}
	if err := m.reload(); err != nil {
		return false, err
	}
	return true, nil
}

func (m *MaxMind) reload() error {
	info, err := statFile(m.path)
	if err != nil {
		return err
	}
	reader, err := maxminddb.Open(m.path)
	if err != nil {
		return fmt.Errorf("open mmdb: %w", err)
	}
	if err := reader.Verify(); err != nil {
		_ = reader.Close()
		return fmt.Errorf("verify mmdb: %w", err)
	}
	version := fmt.Sprintf("%s@%s", reader.Metadata.DatabaseType, reader.Metadata.BuildTime().UTC().Format(time.RFC3339))

	m.mu.Lock()
	old := m.reader
	m.reader = reader
	m.version = version
	m.modTime = info.ModTime()
	m.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return nil
}
