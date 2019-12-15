package server

import (
	"bytes"
	"fmt"
	"math/rand"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"time"
)

const (
	UDP_PACKET_SIZE = 1500
	MSEARCH_HEADER  = "M-SEARCH * HTTP/1.1\r\n"
	SSDP_ADDR       = "239.255.255.250:1900"
)

func serveSSDP(httpPort int) {
	maddr, err := net.ResolveUDPAddr("udp", SSDP_ADDR)
	if err != nil {
		panic(err)
	}
	conn, err := net.ListenMulticastUDP("udp4", nil, maddr)
	if err != nil {
		panic(err)
	}

	logger.Println("Listening to SSDP")

	// SSDP packets may at most be one UDP packet
	buf := make([]byte, UDP_PACKET_SIZE)

	for {
		n, raddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			panic(err)
		}

		packet := buf[:n]

		if !bytes.HasPrefix(packet, []byte(MSEARCH_HEADER)) {
			continue
		}


		msg, err := mail.ReadMessage(bytes.NewReader(packet[len(MSEARCH_HEADER):]))
		if err != nil {
			// ignore malformed packet
			continue
		}

		if !strings.HasPrefix(msg.Header.Get("ST"), "urn:dial-multiscreen-org:service:dial:") {
			// not the request we're looking for
			// TODO this is not UPnP compliant: it needs to respond to various other requests as well like ssdp:any.
			// On the other hand, the DIAL specification seems to imply this is the only required "ST"
			// that needs to be responded to.
			continue
		}
		
		logger.Println("M-SEARCH from %s", raddr)
		
		go serveSSDPResponse(msg, conn, raddr, httpPort)
	}

	defer conn.Close()
}

func serveSSDPResponse(msg *mail.Message, conn *net.UDPConn, raddr *net.UDPAddr, httpPort int) {
	mx, err := strconv.Atoi(msg.Header.Get("MX"))

	if err != nil {
		logger.Warnln("could  not parse MX header:", err)
		return
	}

	time.Sleep(time.Duration(rand.Int31n(1000000)) * time.Duration(mx) * time.Microsecond)
	
	// Only for getting local ip
	ipconn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		panic(err)
	}
	defer ipconn.Close()

	// TODO implement OS header, BOOTID.UPNP.ORG
	// and make this a real template
	response := fmt.Sprintf("HTTP/1.1 200 OK\r\n"+
		"CACHE-CONTROL: max-age=1800\r\n"+
		"DATE: %s\r\n"+
		"EXT: \r\n"+
		"LOCATION: http://%s:%d/upnp/description.xml\r\n"+
		"SERVER: Linux/2.6.16+ UPnP/1.1 %s/%s\r\n"+
		"ST: urn:dial-multiscreen-org:service:dial:1\r\n"+
                "USN: uuid:%s::urn:dial-multiscreen-org:service:dial:1\r\n"+
		"CONFIGID.UPNP.ORG: %d\r\n"+
		"\r\n", time.Now().Format(time.RFC1123), getUrlIP(ipconn.LocalAddr()), httpPort, NAME, VERSION, deviceUUID, CONFIGID)

	_, err = conn.WriteTo([]byte(response), raddr)

	ipconn.Close()
	logger.Println("Sent SSDP response")

	if err != nil {
		panic(err)
	}
}
