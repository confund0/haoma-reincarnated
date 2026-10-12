package io.haoma.calculator.messenger

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale


class PendingTextsTest {

    private val reason = "No secure session with this contact yet. Try again in a moment."

    private fun pending(id: Long, chat: String = "c1") = PendingText(id, chat, "text $id", 100L + id)

    @Test fun aFailedTextStaysWithTheReason() {
        var m = addPendingText(emptyMap(), pending(1))
        m = markPendingFailed(m, "c1", 1, reason)
        val list = m["c1"].orEmpty()
        assertEquals(1, list.size)
        assertTrue(list[0].isFailed)
        assertEquals(reason, list[0].failedReason)
        assertEquals(DeliveryState.FAILED, list[0].asTimelineEvent().deliveryState)
    }

    @Test fun aSentTextWaitsForItsRowThenGoes() {
        var m = addPendingText(emptyMap(), pending(1))
        m = markPendingSent(m, "c1", 1, "m-1", rowPresent = false)
        assertEquals("m-1", m["c1"]!!.single().msgId)
        assertEquals(DeliveryState.PENDING, m["c1"]!!.single().asTimelineEvent().deliveryState)
        m = dropPendingForRow(m, "c1", "m-1")
        assertNull(m["c1"])
    }

    @Test fun aSentTextWhoseRowIsThereGoesAtOnce() {
        var m = addPendingText(emptyMap(), pending(1))
        m = addPendingText(m, pending(2))
        m = markPendingSent(m, "c1", 1, "m-1", rowPresent = true)
        assertEquals(listOf(2L), m["c1"]!!.map { it.localId })
    }

    @Test fun aRowForAnotherTextDropsNothing() {
        var m = addPendingText(emptyMap(), pending(1))
        m = markPendingSent(m, "c1", 1, "m-1", rowPresent = false)
        assertEquals(m, dropPendingForRow(m, "c1", "m-other"))
        assertEquals(m, dropPendingForRow(m, "c1", ""))
    }

    @Test fun aReloadDropsTheTextsTheDaemonSaidYesTo() {
        var m = addPendingText(emptyMap(), pending(1))
        m = addPendingText(m, pending(2))
        m = addPendingText(m, pending(3))
        m = markPendingSent(m, "c1", 1, "m-1", rowPresent = false)
        m = markPendingFailed(m, "c1", 3, reason)
        m = dropSentPendingTexts(m, "c1")
        assertEquals(listOf(2L, 3L), m["c1"]!!.map { it.localId })
    }

    private fun chat(waiting: Boolean, startedAt: Long, contact: Boolean = false): ChatEntry = ChatEntry.fromJson(
        JSONObject()
            .put("chat_id", "c1")
            .put("kind", "direct")
            .put("waiting_for_session", waiting)
            .put("waiting_for_contact", contact)
            .put("chat_started_at", startedAt),
    )

    @Test fun theTopLineFollowsTheDaemon() {
        assertEquals(SYSTEM_LINE_NO_SESSION, chatSystemLine(chat(true, 0L)))
        assertEquals(SYSTEM_LINE_NO_ANSWER, chatSystemLine(chat(false, 0L, contact = true)))
        val at = 1_700_000_000L
        val want = "Chat started " + SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.US).format(Date(at * 1000L))
        assertEquals(want, chatSystemLine(chat(false, at)))
        assertNull(chatSystemLine(chat(false, 0L)))
        assertNull(chatSystemLine(null))
    }
}
