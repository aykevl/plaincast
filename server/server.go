package server

import (
	"log"
	"flag"

	"github.com/nu7hatch/gouuid"
)

const (
	NAME          = "Plaincast"
	FRIENDLY_NAME = "Plaincast"
	VERSION       = "0.0.1"
	CONFIGID      = 1
)

var deviceUUID *uuid.UUID

var disableSSDP = flag.Bool("no-ssdp", false, "disable SSDP broadcast")

func Serve() {
	var err error
	deviceUUID, err = getUUID()
	if err != nil {
		log.Fatal(err)
	}

	us := NewUPnPServer()
	httpPort, err := us.startServing()
	if err != nil {
		log.Fatal(err)
	}
	log.Println("serving HTTP on port", httpPort)

	if !*disableSSDP {
		serveSSDP(httpPort)
	} else {
		// wait forever
		select {}
	}
}
