package server

import (
	"flag"

	"github.com/aykevl/plaincast/log"
	"github.com/nu7hatch/gouuid"
)

const (
	NAME          = "Plaincast"
	FRIENDLY_NAME = "Plaincast"
	VERSION       = "0.0.1"
	CONFIGID      = 1
)

var deviceUUID *uuid.UUID
var disableSSDP = flag.Bool("no-ssdp", false, "Disable SSDP server")
var logger = log.New("server", "Log HTTP and SSDP server")

func Serve() {
	var err error
	deviceUUID, err = getUUID()
	if err != nil {
		logger.Fatal(err)
	}

	us := NewUPnPServer()
	httpPort, err := us.startServing()
	if err != nil {
		logger.Fatal(err)
	}
	logger.Println("Serving HTTP on port", httpPort)

	if !*disableSSDP {
		serveSSDP(httpPort)
	} else {
		// wait forever
		select {}
	}
}
