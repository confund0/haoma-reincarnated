package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"haoma/internal/tor/control"
)

const localAddr = "127.0.0.1:12345"

func main() {
	ctrlAddr := os.Getenv("HAOMA_REAL_TOR_CTRL")
	pass := os.Getenv("HAOMA_REAL_TOR_PASS")
	if ctrlAddr == "" {
		log.Fatal("set HAOMA_REAL_TOR_CTRL")
	}

	srv := &http.Server{
		Addr: localAddr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, "hello from haoma — tor control library is live.")
		}),
	}
	go func() {
		log.Printf("local http listening on http://%s", localAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

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

	o, err := c.AddOnionNew([]control.OnionPort{{VirtPort: 80, Target: localAddr}})
	if err != nil {
		log.Fatalf("AddOnionNew: %v", err)
	}

	fmt.Printf("\n  open in Tor Browser:  http://%s.onion\n", o.ServiceID)
	fmt.Printf("  (tor often needs ~30-60s to publish the HS descriptor)\n\n")
	fmt.Println("  Ctrl-C to tear down.")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	fmt.Println("\ntearing down…")
	if err := c.DelOnion(o.ServiceID); err != nil {
		log.Printf("DelOnion: %v", err)
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	fmt.Println("done.")
}
