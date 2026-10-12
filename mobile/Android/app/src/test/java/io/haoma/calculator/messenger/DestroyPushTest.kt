package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.Frame
import io.haoma.calculator.core.ipc.FrameType
import java.util.TimeZone
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test


class DestroyPushTest {

    private var savedZone: TimeZone? = null

    
    @Before
    fun pinTheZone() {
        savedZone = TimeZone.getDefault()
        TimeZone.setDefault(TimeZone.getTimeZone("UTC"))
    }

    @After
    fun restoreTheZone() {
        savedZone?.let { TimeZone.setDefault(it) }
    }

    
    private class Teardown {
        val ran = AtomicInteger(0)
        val latch = CountDownLatch(1)

        
        val sentenceAtTeardown = AtomicReference<String?>(null)
    }

    
    private class Clear {
        val ran = AtomicInteger(0)

        
        val sentenceAtClear = AtomicReference<String?>(null)
    }

    private fun storeWith(teardown: Teardown, clear: Clear? = null): MessengerStore {
        
        
        val holder = AtomicReference<MessengerStore>()
        val timings = DestroyTimings(
            screenGraceMs = 5L,
            tearDown = {
                teardown.sentenceAtTeardown.set(holder.get()?._destroySentence?.value)
                teardown.ran.incrementAndGet()
                teardown.latch.countDown()
            },
        )
        val store = MessengerStore(
            destroyTimings = if (clear == null) {
                timings
            } else {
                timings.copy(
                    clearLiveState = {
                        clear.sentenceAtClear.set(it._destroySentence.value)
                        clear.ran.incrementAndGet()
                    },
                )
            },
        )
        holder.set(store)
        return store
    }

    private fun destroyFrame(reason: String?) = Frame(
        type = FrameType.DestroyDevice,
        payload = reason?.let { JSONObject().put("reason", it) },
    )

    private fun attemptsFrame(count: Int, lastAt: Long) = Frame(
        type = FrameType.DestroyAttemptsSeen,
        payload = JSONObject().put("count", count).put("last_at", lastAt),
    )

    private fun awaitTeardown(t: Teardown) {
        assertTrue("teardown never ran", t.latch.await(5, TimeUnit.SECONDS))
    }

    
    @Test
    fun `the erase push shows the reason it carries`() {
        val t = Teardown()
        val store = storeWith(t)

        store.dispatch(destroyFrame("a linked device commanded this one to erase itself"))
        awaitTeardown(t)

        assertEquals(
            "This device is being erased: a linked device commanded this one to erase itself",
            store.destroySentence.value,
        )
    }

    
    @Test
    fun `an empty reason becomes the standing sentence`() {
        val t = Teardown()
        val store = storeWith(t)

        store.dispatch(destroyFrame(""))
        awaitTeardown(t)

        assertEquals(
            "This device is being erased: the other device commanded it",
            store.destroySentence.value,
        )
    }

    
    @Test
    fun `an erase push with no payload still tears down`() {
        val t = Teardown()
        val store = storeWith(t)

        store.dispatch(destroyFrame(null))
        awaitTeardown(t)

        assertEquals(1, t.ran.get())
        assertEquals(
            "This device is being erased: the other device commanded it",
            store.destroySentence.value,
        )
    }

    
    @Test
    fun `the sentence is written before the teardown runs`() {
        val t = Teardown()
        val store = storeWith(t)

        store.dispatch(destroyFrame("the other device commanded it"))
        awaitTeardown(t)

        assertNotNull(
            "the teardown ran before the sentence was written",
            t.sentenceAtTeardown.get(),
        )
        assertEquals(
            "This device is being erased: the other device commanded it",
            t.sentenceAtTeardown.get(),
        )
    }

    
    @Test
    fun `a second erase push tears down once`() {
        val t = Teardown()
        val store = storeWith(t)

        store.dispatch(destroyFrame("first"))
        store.dispatch(destroyFrame("second"))
        awaitTeardown(t)
        
        
        Thread.sleep(300L)

        assertEquals(1, t.ran.get())
        assertEquals("This device is being erased: first", store.destroySentence.value)
    }

    
    @Test
    fun `a zero count produces no notice`() {
        val notices = produceNotices(
            passphraseIsDefault = false,
            destroyAttempts = DestroyAttemptsSeenPush(count = 0, lastAt = 1_754_903_010L),
            snooze = emptyMap(),
        )
        assertTrue("a zero count reached a screen", notices.isEmpty())
    }

    
    @Test
    fun `no attempts push produces no notice`() {
        val notices = produceNotices(
            passphraseIsDefault = false,
            destroyAttempts = null,
            snooze = emptyMap(),
        )
        assertTrue(notices.isEmpty())
    }

    
    @Test
    fun `a count produces a notice saying how many and when`() {
        val notices = produceNotices(
            passphraseIsDefault = false,
            destroyAttempts = DestroyAttemptsSeenPush(count = 3, lastAt = 1_754_903_010L),
            snooze = emptyMap(),
        )
        assertEquals(1, notices.size)
        val n = notices[0]
        assertEquals(NOTICE_DESTROY_ATTEMPTS, n.id)
        assertEquals("Failed destroy attempts", n.title)
        assertEquals(
            "Somebody tried the destroy PIN on this device. 3 wrong attempts. " +
                "The last one was at 2025-08-11 09:03.",
            n.body,
        )
    }

    
    @Test
    fun `a single attempt reads in the singular`() {
        assertEquals(
            "Somebody tried the destroy PIN on this device. One wrong attempt. " +
                "It was at 2025-08-11 09:03.",
            destroyAttemptsBody(count = 1, lastAt = 1_754_903_010L),
        )
    }

    
    @Test
    fun `an unstated time leaves the time out`() {
        assertEquals(
            "Somebody tried the destroy PIN on this device. 2 wrong attempts.",
            destroyAttemptsBody(count = 2, lastAt = 0L),
        )
    }

    
    @Test
    fun `the attempts push reaches the producer through the reducer`() {
        val store = MessengerStore()
        assertNull(store._noticeDestroyAttempts.value)

        store.dispatch(attemptsFrame(count = 2, lastAt = 1_754_903_010L))

        assertEquals(2, store._noticeDestroyAttempts.value?.count)
        assertEquals(1_754_903_010L, store._noticeDestroyAttempts.value?.lastAt)
        assertEquals(
            1,
            produceNotices(false, store._noticeDestroyAttempts.value, emptyMap()).size,
        )
    }

    
    @Test
    fun `the unlock keys, clipboard and call banner are cleared before the teardown`() =
        kotlinx.coroutines.runBlocking {
            val order = mutableListOf<String>()
            runDestroy(
                reason = "the operator asked",
                showSentence = { order.add("sentence") },
                clearLiveState = { order.add("clear") },
                graceMs = 0L,
                tearDown = { order.add("teardown") },
            )
            assertEquals(listOf("sentence", "clear", "teardown"), order)
        }

    
    @Test
    fun `the erase push reaches the clearing`() {
        val t = Teardown()
        val c = Clear()
        val store = storeWith(t, c)

        store.dispatch(destroyFrame("the other device commanded it"))
        awaitTeardown(t)

        assertEquals("the push did not reach the clearing", 1, c.ran.get())
        assertEquals(
            "the clearing ran before the sentence was written",
            "This device is being erased: the other device commanded it",
            c.sentenceAtClear.get(),
        )
    }

    
    @Test
    fun `the erase push reads the daemon's key for this erasure`() {
        val parsed = DestroyDevicePush.fromJson(
            JSONObject()
                .put("reason", "the other device commanded it")
                .put("erase_id", "3f9c2ab7d1e04856"),
        )
        assertEquals("3f9c2ab7d1e04856", parsed.eraseId)
        assertEquals("the other device commanded it", parsed.reason)
    }

    
    @Test
    fun `a push with no erase id still parses and still tears down`() {
        val parsed = DestroyDevicePush.fromJson(
            JSONObject().put("reason", "the other device commanded it"),
        )
        assertEquals("", parsed.eraseId)

        val t = Teardown()
        val c = Clear()
        val store = storeWith(t, c)
        store.dispatch(destroyFrame("the other device commanded it"))
        awaitTeardown(t)

        assertEquals("an erasure with no id did not clear", 1, c.ran.get())
        assertEquals(1, t.ran.get())
    }

    
    @Test
    fun `the notice carries its snooze`() {
        val notices = produceNotices(
            passphraseIsDefault = false,
            destroyAttempts = DestroyAttemptsSeenPush(count = 1, lastAt = 0L),
            snooze = mapOf(NOTICE_DESTROY_ATTEMPTS to NoticeSnoozeEntry(until = 4_242L, step = 0)),
        )
        assertEquals(4_242L, notices[0].snoozeUntil)
    }
}
