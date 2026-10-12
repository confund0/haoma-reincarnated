package io.haoma.calculator.messenger.contacts

import io.haoma.calculator.messenger.PeerEntry


fun contactsToDraw(peers: List<PeerEntry>): List<PeerEntry> = peers.filterNot { it.sibling }
