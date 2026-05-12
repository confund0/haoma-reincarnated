package main

import (
	"log/slog"
	"sync"

	"haoma/internal/eventbus"
)

type externalReachPayload struct {
	Ok             bool   `json:"ok"`
	LastTargetName string `json:"last_target,omitempty"`
	At             int64  `json:"at"`
}

var extprobeBurstMu sync.Mutex

func (d *daemon) runExtProbeBurst() {
	if d.extProbe == nil {
		slog.Warn("extprobe: burst requested but prober nil — tor not up yet")
		return
	}
	if !extprobeBurstMu.TryLock() {
		slog.Debug("extprobe: burst dropped — another burst in flight")
		return
	}
	go func() {
		defer extprobeBurstMu.Unlock()
		res := d.extProbe.Burst(d.bgCtx)
		slog.Info("extprobe: burst complete",
			slog.Bool("ok", res.Ok),
			slog.String("last_target", res.LastTargetName),
		)
		if d.bus != nil {
			d.bus.Publish(eventbus.TopicHealthExternalReachChanged, externalReachPayload{
				Ok:             res.Ok,
				LastTargetName: res.LastTargetName,
				At:             res.At.Unix(),
			})
		}
	}()
}
