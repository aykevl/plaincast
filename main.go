package main

import (
	"log"

	"github.com/aykevl93/plaincast/server"
)

func main() {
	log.SetFlags(log.Ltime)

	server.Serve()
}
