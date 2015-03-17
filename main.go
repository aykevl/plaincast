package main

import (
	"flag"
	"log"

	"github.com/aykevl93/plaincast/server"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	flag.Parse()

	server.Serve()
}
