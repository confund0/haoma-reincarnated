package main

type peerPresenceObservation struct {
	PeerID string `json:"peer_id"`
	Source string `json:"source"`
	At     int64  `json:"at"`
}

type peerLastSeenObservation struct {
	PeerID        string `json:"peer_id"`
	LastActiveAt  int64  `json:"last_active_at"`
	LastPassiveAt int64  `json:"last_passive_at"`
}
