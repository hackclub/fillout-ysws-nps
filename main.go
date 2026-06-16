package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	addr := ":" + port()

	log.Printf("starting server on %s", addr)
	if err := http.ListenAndServe(addr, newRouter()); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// port returns the port to listen on, defaulting to 8080.
func port() string {
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return "8080"
}
