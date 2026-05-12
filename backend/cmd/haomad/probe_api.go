package main

import (
	"errors"
	"net/http"

	"haoma/internal/peers"
)

func (d *daemon) handleExternalProbeBurst(w http.ResponseWriter, r *http.Request) {
	if d.extProbe == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("tor not yet up"))
		return
	}
	d.runExtProbeBurst()
	w.WriteHeader(http.StatusAccepted)
}

func (d *daemon) handlePeerSelfProbe(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, errors.New("peer id required"))
		return
	}
	peer, err := d.registry.Get(id)
	if err != nil {
		if errors.Is(err, peers.ErrPeerNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if peer.MyOnionAddr == "" {
		writeErr(w, http.StatusServiceUnavailable, errors.New("peer has no my-onion address"))
		return
	}
	state := d.probePeerSelf(r.Context(), peer.ID, peer.MyOnionAddr, false)
	writeJSON(w, http.StatusOK, peerSelfReachPayload{
		PeerID: state.PeerID,
		Onion:  state.Onion,
		Ok:     state.Ok,
		At:     state.At.Unix(),
	})
}
