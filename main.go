package main

import (
	"flag"

	"github.com/aykevl/plaincast/server"
)

func main() {
	flag.Parse()

	server.Serve()
}
