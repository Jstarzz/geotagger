package geo

import "net/netip"

type Result struct {
	Country     string
	CountryCode string
}

type Lookup interface {
	Lookup(netip.Addr) (Result, bool, error)
	Version() string
	Close() error
}
