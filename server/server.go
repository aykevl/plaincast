package server

import (
	"fmt"

	"github.com/nu7hatch/gouuid"
)

const (
	NAME          = "YouTube-receiver"
	FRIENDLY_NAME = "YouTube receiver"
	VERSION       = "0.0.1"
	CONFIGID      = 1
)

var deviceUUID *uuid.UUID

func Serve() {
	var err error
	deviceUUID, err = getUUID()
	if err != nil {
		panic(err)
	}

	us := NewUPnPServer()
	httpPort, err := us.startServing()
	if err != nil {
		panic(err)
	}
	fmt.Println("serving HTTP on port", httpPort)

	serveSSDP(httpPort)
}
