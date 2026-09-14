package maxmindgeo

import (
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/Jstarzz/geotagger/internal/geo"

	"github.com/oschwald/maxminddb-golang/v2"
)

type MaxMind struct {
	mu sync.RWMutex

	cityPath    string
	asnPath     string
	cityReader  *maxminddb.Reader
	asnReader   *maxminddb.Reader
	cityVersion string
	asnVersion  string
	cityModTime time.Time
	asnModTime  time.Time
}

type cityRecord struct {
	Continent struct {
		Code  string `maxminddb:"code"`
		Names struct {
			English string `maxminddb:"en"`
		} `maxminddb:"names"`
	} `maxminddb:"continent"`
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
		Names   struct {
			English string `maxminddb:"en"`
		} `maxminddb:"names"`
	} `maxminddb:"country"`
	Subdivisions []struct {
		ISOCode string `maxminddb:"iso_code"`
		Names   struct {
			English string `maxminddb:"en"`
		} `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
	City struct {
		Names struct {
			English string `maxminddb:"en"`
		} `maxminddb:"names"`
	} `maxminddb:"city"`
	Postal struct {
		Code string `maxminddb:"code"`
	} `maxminddb:"postal"`
	Location struct {
		AccuracyRadius uint16   `maxminddb:"accuracy_radius"`
		Latitude       *float64 `maxminddb:"latitude"`
		Longitude      *float64 `maxminddb:"longitude"`
		TimeZone       string   `maxminddb:"time_zone"`
	} `maxminddb:"location"`
}

type asnRecord struct {
	AutonomousSystemNumber       uint32 `maxminddb:"autonomous_system_number"`
	AutonomousSystemOrganization string `maxminddb:"autonomous_system_organization"`
}

func OpenMaxMind(cityPath, asnPath string) (*MaxMind, error) {
	m := &MaxMind{cityPath: cityPath, asnPath: asnPath}
	if err := m.reload(true, true); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *MaxMind) Lookup(ip netip.Addr) (geo.Result, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := geo.Result{
		IP:                 ip.String(),
		CityDatabaseVersion: m.cityVersion,
		ASNDatabaseVersion:  m.asnVersion,
	}

	cityLookup := m.cityReader.Lookup(ip)
	var city cityRecord
	if err := cityLookup.Decode(&city); err != nil {
		return geo.Result{}, false, err
	}
	cityFound := city.Country.ISOCode != "" || city.Country.Names.English != "" || city.City.Names.English != ""
	if cityFound {
		result.ContinentCode = city.Continent.Code
		result.Continent = city.Continent.Names.English
		result.CountryCode = city.Country.ISOCode
		result.Country = city.Country.Names.English
		if len(city.Subdivisions) > 0 {
			result.RegionCode = city.Subdivisions[0].ISOCode
			result.Region = city.Subdivisions[0].Names.English
		}
		result.City = city.City.Names.English
		result.PostalCode = city.Postal.Code
		result.Latitude = city.Location.Latitude
		result.Longitude = city.Location.Longitude
		result.AccuracyRadiusKM = city.Location.AccuracyRadius
		result.TimeZone = city.Location.TimeZone
		if prefix := cityLookup.Prefix(); prefix.IsValid() {
			result.Network = prefix.String()
		}
	}

	asnLookup := m.asnReader.Lookup(ip)
	var asn asnRecord
	if err := asnLookup.Decode(&asn); err != nil {
		return geo.Result{}, false, err
	}
	asnFound := asn.AutonomousSystemNumber != 0 || asn.AutonomousSystemOrganization != ""
	if asnFound {
		result.ASN = asn.AutonomousSystemNumber
		result.ASNOrganization = asn.AutonomousSystemOrganization
		if result.Network == "" {
			if prefix := asnLookup.Prefix(); prefix.IsValid() {
				result.Network = prefix.String()
			}
		}
	}

	return result, cityFound || asnFound, nil
}

func (m *MaxMind) Version() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return fmt.Sprintf("city=%s;asn=%s", m.cityVersion, m.asnVersion)
}

func (m *MaxMind) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var firstErr error
	if m.cityReader != nil {
		if err := m.cityReader.Close(); err != nil {
			firstErr = err
		}
		m.cityReader = nil
	}
	if m.asnReader != nil {
		if err := m.asnReader.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		m.asnReader = nil
	}
	return firstErr
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
	cityInfo, err := statFile(m.cityPath)
	if err != nil {
		return false, err
	}
	asnInfo, err := statFile(m.asnPath)
	if err != nil {
		return false, err
	}

	m.mu.RLock()
	cityChanged := cityInfo.Size() > 0 && cityInfo.ModTime().After(m.cityModTime)
	asnChanged := asnInfo.Size() > 0 && asnInfo.ModTime().After(m.asnModTime)
	m.mu.RUnlock()
	if !cityChanged && !asnChanged {
		return false, nil
	}
	if err := m.reload(cityChanged, asnChanged); err != nil {
		return false, err
	}
	return true, nil
}

func (m *MaxMind) reload(cityChanged, asnChanged bool) error {
	var newCity, newASN *maxminddb.Reader
	var cityVersion, asnVersion string
	var cityModTime, asnModTime time.Time
	var err error

	if cityChanged {
		newCity, cityVersion, cityModTime, err = openVerified(m.cityPath)
		if err != nil {
			return fmt.Errorf("open city mmdb: %w", err)
		}
	}
	if asnChanged {
		newASN, asnVersion, asnModTime, err = openVerified(m.asnPath)
		if err != nil {
			if newCity != nil {
				_ = newCity.Close()
			}
			return fmt.Errorf("open ASN mmdb: %w", err)
		}
	}

	m.mu.Lock()
	oldCity := m.cityReader
	oldASN := m.asnReader
	if cityChanged {
		m.cityReader = newCity
		m.cityVersion = cityVersion
		m.cityModTime = cityModTime
	}
	if asnChanged {
		m.asnReader = newASN
		m.asnVersion = asnVersion
		m.asnModTime = asnModTime
	}
	m.mu.Unlock()

	if cityChanged && oldCity != nil {
		_ = oldCity.Close()
	}
	if asnChanged && oldASN != nil {
		_ = oldASN.Close()
	}
	return nil
}

func openVerified(path string) (*maxminddb.Reader, string, time.Time, error) {
	info, err := statFile(path)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	reader, err := maxminddb.Open(path)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	if err := reader.Verify(); err != nil {
		_ = reader.Close()
		return nil, "", time.Time{}, fmt.Errorf("verify mmdb: %w", err)
	}
	version := fmt.Sprintf("%s@%s", reader.Metadata.DatabaseType, reader.Metadata.BuildTime().UTC().Format(time.RFC3339))
	return reader, version, info.ModTime(), nil
}
