package crawler

import (
	"net"
	"time"

	"github.com/decred/dcrd/wire"
)

type Node struct {
	Services        wire.ServiceFlag
	LastAttempt     time.Time
	LastSuccess     time.Time
	ProtocolVersion uint32
	IP              net.IP
	UserAgent       string
	GeoData         *GeoData
}

type GeoData struct {
	City      string
	Region    string
	Country   string
	Continent string
	Org       string
	ISP       string
	AS        string
	ASName    string
	Lat       float32
	Lon       float32
}
