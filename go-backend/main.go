package main

import (
	"flag"
	"log"
)

var (
	port = flag.Int("port", 1443, "TCP port to listen on")
)

func main() {
	flag.Parse()

	log.Printf("Starting server on port %d", *port)
}
