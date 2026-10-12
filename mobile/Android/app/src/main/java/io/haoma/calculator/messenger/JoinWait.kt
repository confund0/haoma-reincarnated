package io.haoma.calculator.messenger

import kotlin.time.Duration
import kotlin.time.Duration.Companion.seconds


const val JOINER_DIAL_SECONDS: Long = 120L


internal val PAIR_ONION_ACCEPT_TIMEOUT: Duration = (JOINER_DIAL_SECONDS + 60L).seconds


const val DEVICE_LINK_REGISTER_SECONDS: Long = 30L
const val DEVICE_LINK_BUNDLE_SECONDS: Long = 180L


internal val DEVICE_LINK_ACCEPT_TIMEOUT: Duration =
    (JOINER_DIAL_SECONDS + DEVICE_LINK_REGISTER_SECONDS + DEVICE_LINK_BUNDLE_SECONDS + 60L).seconds


const val TOR_SLOW = "Tor is slow at the moment. Try again in a couple of minutes."


const val JOIN_TOR_SLOW =
    "Tor is slow at the moment. Try again in a couple of minutes, or check the seven words."


const val JOIN_WORDS_WRONG = "One of the words is wrong, so check the seven words and try again."


const val JOIN_INVITATION_SPENT = "This invitation was already used or has run out, so ask for a new one."


const val PAIR_JOIN_NO_ANSWER =
    "No answer came back, so it is not known whether you are paired. Check your contacts before you try again."


const val JOIN_WAIT_NO_ANSWER = "Waiting for Tor: no answer yet."


const val JOIN_WAIT_LINK_NO_ANSWER = JOIN_WAIT_NO_ANSWER + " Cancel does not stop a join that was already sent."


fun joinSecondsLeft(startedAtMs: Long, nowMs: Long): Long {
    val leftMs = JOINER_DIAL_SECONDS * 1000L - (nowMs - startedAtMs)
    if (leftMs <= 0L) return 0L
    return (leftMs + 999L) / 1000L
}


fun joinWaitLine(startedAtMs: Long, nowMs: Long, link: Boolean = false): String {
    val left = joinSecondsLeft(startedAtMs, nowMs)
    return when (left) {
        0L -> if (link) JOIN_WAIT_LINK_NO_ANSWER else JOIN_WAIT_NO_ANSWER
        1L -> "Waiting for Tor: 1 second left."
        else -> "Waiting for Tor: $left seconds left."
    }
}


fun joinDialFailed(code: String): Boolean =
    code == "onion_accept_failed" || code == "backend_unavailable"


internal const val REASON_JOIN_WORDS_INVALID = "join_words_invalid"


internal const val REASON_JOIN_INVITATION_SPENT = "join_invitation_spent"


fun joinFailureLine(code: String): String? = when {
    joinDialFailed(code) -> JOIN_TOR_SLOW
    code == REASON_JOIN_WORDS_INVALID -> JOIN_WORDS_WRONG
    code == REASON_JOIN_INVITATION_SPENT -> JOIN_INVITATION_SPENT
    else -> null
}
