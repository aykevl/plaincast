package server

import (
	"errors"
	"net"
	"net/http"

	"github.com/nu7hatch/gouuid"
)

// getLocalAddr gets the local address from the specified address.
// It does not truly open a connection (udp doesn't know connections).
func getLocalAddr(req *http.Request) net.Addr {
	raddr, err := net.ResolveUDPAddr("udp", req.RemoteAddr)
	if err != nil {
		panic(err)
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	return conn.LocalAddr()
}

// getUrlIP formats the address so it can be used inside an URL.
// It wraps the IP address inside [ and ] when it's an IPv6 address.
func getUrlIP(addr net.Addr) string {
	var ip net.IP
	switch addr.(type) {
	case *net.UDPAddr:
		ip = addr.(*net.UDPAddr).IP
	default:
		panic("unknown address type")
	}

	addrString := ip.String()
	if ip.To4() == nil {
		// IPv6
		addrString = "[" + addrString + "]"
	}
	return addrString
}

// getUUID returns a stable UUID based on the first MAC address
func getUUID() (*uuid.UUID, error) {
	itfs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	// get the first interface with a MAC address
	for _, itf := range itfs {
		if len(itf.HardwareAddr) == 0 {
			continue
		}

		// this may not be how UUIDv5 is meant to be used...
		id := []byte(itf.HardwareAddr.String() + "-" + NAME)
		return uuid.NewV5(uuid.NamespaceOID, id)
	}

	return nil, errors.New("could not find interface with MAC address")
}
