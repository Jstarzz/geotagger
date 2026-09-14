package geo

import "net/netip"

type Result struct {
	IP                 string
	Network            string
	ContinentCode      string
	Continent           string
	CountryCode        string
	Country            string
	RegionCode         string
	Region              string
	City                string
	PostalCode          string
	Latitude            *float64
	Longitude           *float64
	AccuracyRadiusKM    uint16
	TimeZone            string
	ASN                 uint32
	ASNOrganization     string
	CityDatabaseVersion string
	ASNDatabaseVersion  string
}

type Lookup interface {
	Lookup(netip.Addr) (Result, bool, error)
	Version() string
	Close() error
}
