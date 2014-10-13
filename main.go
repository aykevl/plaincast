package main

import (
	"log"

	"github.com/aykevl93/youtube-receiver/server"
)

func main() {
	log.SetFlags(log.Ltime)

	server.Serve()
}
