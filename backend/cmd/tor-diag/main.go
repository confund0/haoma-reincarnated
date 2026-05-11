package main

import (
	"context"
	"fmt"
	"log"
	"os"
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

	show := func(key string) {
		v, err := c.GetInfo(key)
		if err != nil {
			fmt.Printf("  %-40s  ERROR: %v\n", key, err)
			return
		}
		fmt.Printf("  %-40s  %s\n", key, v)
	}

	fmt.Println("tor state:")
	show("version")
	show("status/bootstrap-phase")
	show("status/circuit-established")
	show("network-liveness")
	show("status/enough-dir-info")

	if err := c.SetEvents("HS_DESC", "STATUS_CLIENT", "STATUS_GENERAL"); err != nil {
		log.Fatalf("SetEvents: %v", err)
	}

	start := time.Now()
	go func() {
		for ev := range c.Events() {
			fmt.Printf("  [t+%05.1fs] %s %s\n",
				time.Since(start).Seconds(),
				ev.Type,
				firstLine(ev.Lines))
		}
	}()

	fmt.Println("\ncreating fresh onion…")
	o, err := c.AddOnionNew([]control.OnionPort{{VirtPort: 80, Target: "127.0.0.1:12345"}})
	if err != nil {
		log.Fatalf("AddOnionNew: %v", err)
	}
	fmt.Printf("  ServiceID: %s\n", o.ServiceID)
	fmt.Printf("  .onion:    http://%s.onion\n\n", o.ServiceID)

	fmt.Println("listening for events (90s)…")
	time.Sleep(90 * time.Second)

	fmt.Println("\ncleaning up…")
	if err := c.DelOnion(o.ServiceID); err != nil {
		log.Printf("DelOnion: %v", err)
	}
}

func firstLine(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}
