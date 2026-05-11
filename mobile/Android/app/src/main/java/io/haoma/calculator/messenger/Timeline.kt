package io.haoma.calculator.messenger


data class Reaction(
    val peerId: String,
    val emoji: String,
    val at: Long,
) {
    val isMine: Boolean get() = peerId.isEmpty()
}


data class TimelineCache(
    val chatId: String,
    val events: List<TimelineEvent> = emptyList(),
    val oldestDisplayTs: Long = 0L,
    val hasMore: Boolean = true,
    val loading: Boolean = false,
    val reactionsByTarget: Map<String, Map<String, Reaction>> = emptyMap(),
)


fun mergeTimelineEvent(cache: TimelineCache, ev: TimelineEvent): TimelineCache {
    val list = cache.events
    val existingIdx = locateEvent(list, ev)
    val merged = if (existingIdx >= 0) {
        val existing = list[existingIdx]
        val nextDelivery =
            if (DeliveryState.rank(ev.deliveryState) >= DeliveryState.rank(existing.deliveryState)) {
                ev.deliveryState
            } else {
                existing.deliveryState
            }
        val patched = ev.copy(deliveryState = nextDelivery)
        list.toMutableList().apply { this[existingIdx] = patched }
    } else {
        (list + ev).sortedWith(compareBy({ it.displayTs }, { it.recvSeq }))
    }
    val withReactions = applyReactionMaybe(cache.reactionsByTarget, ev)
    return cache.copy(
        events = merged,
        oldestDisplayTs = oldestOf(merged),
        reactionsByTarget = withReactions,
    )
}


internal fun applyReactionMaybe(
    current: Map<String, Map<String, Reaction>>,
    ev: TimelineEvent,
): Map<String, Map<String, Reaction>> {
    if (ev.kind != EventKind.REACTION) return current
    val body = ev.body ?: return current
    val target = body.optString("target_msg_id", "")
    if (target.isEmpty()) return current
    val emoji = body.optString("emoji", "")
    val at = body.optLong("at", 0L).takeIf { it > 0L } ?: ev.displayTs
    val peerKey = ev.senderPeerId
    val peerMap = current[target] ?: emptyMap()
    val existing = peerMap[peerKey]
    if (existing != null && existing.at > at) return current
    val nextPeerMap = peerMap.toMutableMap()
    if (emoji.isEmpty()) {
        nextPeerMap.remove(peerKey)
    } else {
        nextPeerMap[peerKey] = Reaction(peerId = peerKey, emoji = emoji, at = at)
    }
    val next = current.toMutableMap()
    if (nextPeerMap.isEmpty()) next.remove(target) else next[target] = nextPeerMap.toMap()
    return next.toMap()
}


fun applyDeliveryStatus(cache: TimelineCache, envelopeId: String, state: String): TimelineCache {
    if (envelopeId.isEmpty()) return cache
    val idx = cache.events.indexOfFirst { it.envelopeId == envelopeId }
    if (idx < 0) return cache
    val existing = cache.events[idx]
    if (existing.deliveryState == DeliveryState.READ && state != DeliveryState.READ) return cache
    if (DeliveryState.rank(state) < DeliveryState.rank(existing.deliveryState)) return cache
    if (existing.deliveryState == state) return cache
    val updated = existing.copy(deliveryState = state)
    val list = cache.events.toMutableList().apply { this[idx] = updated }
    return cache.copy(events = list)
}


fun removeTimelineEvent(cache: TimelineCache, recvSeq: Long): TimelineCache {
    if (recvSeq <= 0L) return cache
    val idx = cache.events.indexOfFirst { it.recvSeq == recvSeq }
    if (idx < 0) return cache
    val removed = cache.events[idx]
    val list = cache.events.toMutableList().apply { removeAt(idx) }
    
    
    val reactions = if (removed.kind == EventKind.REACTION) {
        rebuildReactions(list)
    } else {
        cache.reactionsByTarget
    }
    return cache.copy(
        events = list,
        oldestDisplayTs = oldestOf(list),
        reactionsByTarget = reactions,
    )
}

private fun rebuildReactions(events: List<TimelineEvent>): Map<String, Map<String, Reaction>> {
    var acc: Map<String, Map<String, Reaction>> = emptyMap()
    for (ev in events) {
        if (ev.kind == EventKind.REACTION) {
            acc = applyReactionMaybe(acc, ev)
        }
    }
    return acc
}


fun mergeTimelinePage(cache: TimelineCache, page: TimelinePageResponse): TimelineCache {
    var merged = cache
    for (ev in page.events) {
        merged = mergeTimelineEvent(merged, ev)
    }
    return merged.copy(
        loading = false,
        hasMore = page.hasMore,
        oldestDisplayTs = oldestOf(merged.events),
    )
}

private fun locateEvent(list: List<TimelineEvent>, ev: TimelineEvent): Int {
    if (ev.msgId.isNotEmpty()) {
        val byMsg = list.indexOfFirst { it.msgId == ev.msgId }
        if (byMsg >= 0) return byMsg
    }
    if (ev.recvSeq > 0L) {
        return list.indexOfFirst { it.recvSeq == ev.recvSeq }
    }
    return -1
}

private fun oldestOf(list: List<TimelineEvent>): Long =
    list.minOfOrNull { it.displayTs } ?: 0L
