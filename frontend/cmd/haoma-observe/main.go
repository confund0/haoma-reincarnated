package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/ipcclient"
)

func main() {
	frontendDir := flag.String("frontend-dir", os.ExpandEnv("$HAOMA_ROOT/frontend"), "haoma daemon data dir")
	addr := flag.String("addr", "", "haoma IPC addr, host:port")
	duration := flag.Duration("for", 15*time.Second, "how long to observe")
	flag.Parse()

	if *addr == "" {
		fmt.Fprintln(os.Stderr, "--addr required")
		os.Exit(1)
	}

	client, err := ipcclient.New(ipcclient.Config{
		FrontendDir: *frontendDir,
		Addr:        *addr,
		ClientName:  "smoke-observer",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "client: %v\n", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()
	go func() {
		<-ctx.Done()
		client.Close()
	}()
	go client.Run()

	for f := range client.Incoming() {
		switch f.Type {
		case ipc.FrameWelcome:
			var p ipc.WelcomePayload
			_ = json.Unmarshal(f.Payload, &p)
			fmt.Printf("[welcome] daemon=%s proto=v%d\n", p.DaemonVersion, p.ProtocolVersion)
		case ipc.FramePing:

			pong, _ := ipc.NewFrame(ipc.FramePong, f.ID, nil)
			client.Send(pong)
		case ipc.FrameStatusEvent:
			var p ipc.StatusEventPayload
			_ = json.Unmarshal(f.Payload, &p)
			var ev struct {
				Kind       string `json:"kind"`
				PeerID     string `json:"peer_id"`
				SourceAddr string `json:"source_addr"`
				SlotIdx    int    `json:"slot_idx"`
			}
			_ = json.Unmarshal(p.Event, &ev)
			fmt.Printf("[status]  kind=%s slot=%d peer=%s source=%s\n", ev.Kind, ev.SlotIdx, ev.PeerID, ev.SourceAddr)
		case ipc.FrameInboxEntry:
			var p ipc.InboxEntryPayload
			_ = json.Unmarshal(f.Payload, &p)
			var env struct {
				ID, From, Kind string
				Payload        []byte
			}
			_ = json.Unmarshal(p.Envelope, &env)
			fmt.Printf("[inbox]   id=%s from=%s kind=%s payload=%q\n", env.ID, env.From, env.Kind, string(env.Payload))
		default:
			fmt.Printf("[%s] %s\n", f.Type, string(f.Payload))
		}
	}
}
