package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port

	store := NewMemoryStorage()
	srv := NewServer(store)

	fmt.Printf("Campaign Events backend listening on %s\n", addr)
	fmt.Printf("Endpoints:\n")
	fmt.Printf("  POST %s/events\n", addr)
	fmt.Printf("  GET  %s/campaigns/{campaign_id}/stats\n", addr)
	fmt.Printf("  GET  %s/campaigns/{campaign_id}/events (paginated)\n", addr)

	if err := http.ListenAndServe(addr, srv); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}

