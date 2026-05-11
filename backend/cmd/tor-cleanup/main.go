package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"haoma/internal/tor/control"
)

func main() {
	ctrlAddr := os.Getenv("HAOMA_REAL_TOR_CTRL")
	pass := os.Getenv("HAOMA_REAL_TOR_PASS")
	if ctrlAddr == "" {
		log.Fatal("set HAOMA_REAL_TOR_CTRL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := control.Dial(ctx, ctrlAddr)
	if err != nil {
		log.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Authenticate(pass); err != nil {
		log.Fatalf("Authenticate: %v", err)
	}

	v, err := c.GetInfo("onions/detached")
	if err != nil {
		log.Fatalf("GETINFO onions/detached: %v", err)
	}
	v = strings.TrimSpace(v)
	if v == "" {
		fmt.Println("no detached onions — nothing to clean up.")
		return
	}

	ids := strings.Split(v, "\n")
	fmt.Printf("found %d detached onion(s):\n", len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		fmt.Printf("  %s.onion — ", id)
		if err := c.DelOnion(id); err != nil {
			fmt.Printf("FAIL: %v\n", err)
		} else {
			fmt.Println("deleted")
		}
	}
}
