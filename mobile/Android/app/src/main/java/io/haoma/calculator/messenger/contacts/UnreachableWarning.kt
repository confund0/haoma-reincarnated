package io.haoma.calculator.messenger.contacts

import io.haoma.calculator.messenger.PeerEntry


const val UNREACHABLE_WARNING =
    "This contact does not answer. If you know they are gone, use Delete peer at the " +
        "bottom of this page. THIS ALSO DELETES THE CHAT HISTORY."


fun detailWarningFor(peer: PeerEntry): String? =
    if (peer.unreachable && peer.retiredAt == 0L) UNREACHABLE_WARNING else null
