package main

import (
	"flag"

	"github.com/aykevl93/plaincast/server"
)

func main() {
	flag.Parse()

	server.Serve()
}
