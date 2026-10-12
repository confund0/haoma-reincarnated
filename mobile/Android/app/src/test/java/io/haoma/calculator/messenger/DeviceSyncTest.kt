package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.Frame
import io.haoma.calculator.core.ipc.FrameType
import java.util.Collections
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicLong
import kotlin.time.Duration
import kotlin.time.Duration.Companion.seconds
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test


class DeviceSyncTest {

    
    private class FakeDaemon {
        class Pending(val type: String, val payload: JSONObject?, val timeout: Duration, val reply: CompletableDeferred<Frame>)

        val pending = LinkedBlockingQueue<Pending>()
        val sent: MutableList<String> = Collections.synchronizedList(mutableListOf())

        suspend fun handle(type: String, payload: JSONObject?, timeout: Duration): Frame {
            sent.add(type)
            val d = CompletableDeferred<Frame>()
            pending.add(Pending(type, payload, timeout, d))
            return d.await()
        }

        fun next(): Pending =
            pending.poll(5, TimeUnit.SECONDS) ?: throw AssertionError("no request reached the daemon")

        fun nothingSent(what: String) {
            val extra = pending.poll(300, TimeUnit.MILLISECONDS)
            assertTrue(what, extra == null)
        }
    }

    private val collectors = mutableListOf<Job>()

    @After fun stopCollecting() {
        collectors.forEach { it.cancel() }
    }

    
    private val clock = AtomicLong(1_754_900_000L)

    private fun store(daemon: FakeDaemon, connected: Boolean = true) = MessengerStore(
        timedRequestSender = { type, payload, timeout -> daemon.handle(type, payload, timeout) },
        inheritedPairingTimings = InheritedPairingTimings(nowSeconds = { clock.get() }),
    ).also { it._connection.value = connected }

    
    private fun toastsOf(s: MessengerStore): LinkedBlockingQueue<String> {
        val q = LinkedBlockingQueue<String>()
        collectors += CoroutineScope(Dispatchers.Unconfined).launch(start = CoroutineStart.UNDISPATCHED) {
            s.toasts.collect { q.add(it) }
        }
        return q
    }

    private fun LinkedBlockingQueue<String>.nextToast(): String =
        poll(5, TimeUnit.SECONDS) ?: throw AssertionError("no toast came")

    private fun LinkedBlockingQueue<String>.noToast(what: String) {
        assertNull(what, poll(300, TimeUnit.MILLISECONDS))
    }

    private fun sent(siblings: Int?, full: Boolean = false, alreadyRunning: Boolean = false) = Frame(
        type = FrameType.DeviceSyncSent,
        payload = JSONObject().apply {
            if (siblings != null) put("siblings", siblings)
            if (full) put("full", true)
            if (alreadyRunning) put("already_running", true)
        },
    )

    private fun finished(
        full: Boolean = false,
        siblings: Int = 1,
        answered: Int = 1,
        rows: Int = 0,
        timedOut: Boolean = false,
        snapshot: Int = 0,
        held: Int = 0,
    ) = Frame(
        type = FrameType.DeviceSyncFinished,
        payload = JSONObject().apply {
            if (full) put("full", true)
            put("siblings", siblings)
            put("answered", answered)
            put("rows_applied", rows)
            put("snapshot_applied", snapshot)
            put("held", held)
            if (timedOut) put("timed_out", true)
        },
    )

    private fun refusal(code: String) = Frame(type = FrameType.Error, payload = JSONObject().put("code", code))

    
    private fun settled(s: MessengerStore): InheritedPairing {
        val until = System.currentTimeMillis() + 5_000
        while (System.currentTimeMillis() < until) {
            val st = s.inheritedPairing.value
            if (!st.syncing) return st
            Thread.sleep(5)
        }
        throw AssertionError("the sync request never ended")
    }

    
    private fun startedSync(s: MessengerStore, d: FakeDaemon, toasts: LinkedBlockingQueue<String>, full: Boolean = false) {
        if (full) s.syncEverything() else s.syncMissed()
        d.next().reply.complete(sent(1, full = full))
        assertEquals(if (full) SYNC_REQUESTED_FULL else SYNC_REQUESTED, toasts.nextToast())
        settled(s)
    }

    
    @Test fun aTapAsksWhatWasMissedAndToastsRequestedSync() {
        val d = FakeDaemon()
        val s = store(d)
        val toasts = toastsOf(s)
        s.syncMissed()
        val p = d.next()
        assertEquals(FrameType.DeviceSync, p.type)
        assertEquals("a tap is /sync without all", false, p.payload?.getBoolean("full"))
        assertTrue("the request is given at least 30 s, got ${p.timeout}", p.timeout >= 30.seconds)
        assertTrue(s.inheritedPairing.value.syncing)
        toasts.noToast("nothing is said before the daemon answers")
        p.reply.complete(sent(1))
        assertEquals("Requested sync", toasts.nextToast())
        assertFalse(settled(s).syncing)
        assertEquals(DeviceSyncWait(full = false, sentAt = clock.get()), s.deviceSyncWait.get())
    }

    @Test fun aLongPressAsksForEverythingAndToastsRequestedFullSync() {
        val d = FakeDaemon()
        val s = store(d)
        val toasts = toastsOf(s)
        s.syncEverything()
        val p = d.next()
        assertEquals(FrameType.DeviceSync, p.type)
        assertEquals("a long press is /sync all", true, p.payload?.getBoolean("full"))
        p.reply.complete(sent(1, full = true))
        assertEquals("Requested FULL sync", toasts.nextToast())
        settled(s)
        assertEquals(true, s.deviceSyncWait.get()?.full)
    }

    @Test fun aSecondPressWhileOneIsOutSendsNothing() {
        val d = FakeDaemon()
        val s = store(d)
        s.syncMissed()
        val p = d.next()
        s.syncEverything()
        d.nothingSent("a second press while a request is out must send nothing")
        p.reply.complete(sent(1))
        settled(s)
        assertEquals(1, d.sent.size)
    }

    @Test fun withNoDaemonNothingGoesOutAndATapToastsSo() {
        val d = FakeDaemon()
        val s = store(d, connected = false)
        val toasts = toastsOf(s)
        s.syncMissed()
        d.nothingSent("nothing may be sent into a connection that is down")
        assertEquals(SYNC_NO_DAEMON, toasts.nextToast())
        assertFalse(s.inheritedPairing.value.syncing)
        assertNull(s.deviceSyncWait.get())
    }

    
    @Test fun aSweepAlreadyRunningToastsSoAndOpensNoWait() {
        val d = FakeDaemon()
        val s = store(d)
        val toasts = toastsOf(s)
        s.syncMissed()
        d.next().reply.complete(sent(1, alreadyRunning = true))
        assertEquals("A sync is already running", toasts.nextToast())
        settled(s)
        assertNull("this tap started no sweep", s.deviceSyncWait.get())
    }

    @Test fun noLinkedDevicesToastsThereIsNothingToSyncWith() {
        val d = FakeDaemon()
        val s = store(d)
        val toasts = toastsOf(s)
        s.syncMissed()
        d.next().reply.complete(sent(0))
        assertEquals(SYNC_NO_SIBLINGS, toasts.nextToast())
        settled(s)
        assertNull(s.deviceSyncWait.get())
    }

    @Test fun aMissingCountIsNotReadAsNoLinkedDevices() {
        val d = FakeDaemon()
        val s = store(d)
        val toasts = toastsOf(s)
        s.syncMissed()
        d.next().reply.complete(sent(null))
        val t = toasts.nextToast()
        assertNotEquals(SYNC_NO_SIBLINGS, t)
        assertEquals(SYNC_SENT_UNREADABLE, t)
        settled(s)
    }

    @Test fun aRefusalToastsItsHeadlineAndTheActionComesBack() {
        val d = FakeDaemon()
        val s = store(d)
        val toasts = toastsOf(s)
        s.syncMissed()
        d.next().reply.complete(refusal("bad_payload"))
        assertEquals("The daemon could not read the sync request.", toasts.nextToast())
        assertFalse(settled(s).syncing)
        assertNull(s.deviceSyncWait.get())
    }

    @Test fun anUnknownRefusalToastsItsToken() {
        val d = FakeDaemon()
        val s = store(d)
        val toasts = toastsOf(s)
        s.syncMissed()
        d.next().reply.complete(refusal("backend-unavailable"))
        assertEquals("Sync refused: backend_unavailable", toasts.nextToast())
        settled(s)
    }

    @Test fun encodeFrameSaysTheRequestsMayHaveGoneOut() {
        
        val r = explainDeviceSyncRefusal("encode_frame")
        assertTrue(r.guidance.single().contains("it was asked"))
    }

    @Test fun aDroppedConnectionIsNoAnswerNotAFailure() {
        val d = FakeDaemon()
        val s = store(d)
        val toasts = toastsOf(s)
        s.syncMissed()
        d.next()
        s._connection.value = false
        val t = toasts.nextToast()
        assertEquals("The sync request had no answer.", t)
        
        assertFalse(t.contains("erase"))
        settled(s)
    }

    
    @Test fun theEndOfASyncThisPhoneStartedIsAToastOnce() {
        val d = FakeDaemon()
        val s = store(d)
        val toasts = toastsOf(s)
        startedSync(s, d, toasts)
        clock.addAndGet(40)
        s.dispatch(finished(rows = 3))
        assertEquals("Sync finished: 3 changes", toasts.nextToast())
        assertNull("the wait is closed by the push", s.deviceSyncWait.get())
        s.dispatch(finished(rows = 3))
        toasts.noToast("a second push for the same sync must say nothing")
    }

    @Test fun theEndOfAFullSyncSaysFull() {
        val d = FakeDaemon()
        val s = store(d)
        val toasts = toastsOf(s)
        startedSync(s, d, toasts, full = true)
        s.dispatch(finished(full = true))
        assertEquals("Full sync finished: nothing new", toasts.nextToast())
    }

    @Test fun anAutomaticSweepOrAnotherClientsSyncIsIgnored() {
        val d = FakeDaemon()
        val s = store(d)
        val toasts = toastsOf(s)
        s.dispatch(finished(rows = 5))
        toasts.noToast("a push with no sync started here must say nothing")
    }

    @Test fun aSyncThatFoundOneRunningShowsNoEnd() {
        val d = FakeDaemon()
        val s = store(d)
        val toasts = toastsOf(s)
        s.syncMissed()
        d.next().reply.complete(sent(1, alreadyRunning = true))
        assertEquals(SYNC_ALREADY_RUNNING, toasts.nextToast())
        settled(s)
        s.dispatch(finished())
        toasts.noToast("the running sweep was not started by this tap")
    }

    @Test fun aPushLaterThanTheWaitIsDroppedAndClearsIt() {
        val d = FakeDaemon()
        val s = store(d)
        val toasts = toastsOf(s)
        startedSync(s, d, toasts)
        clock.addAndGet(InheritedPairingTimings().syncFinishWait.inWholeSeconds + 1)
        s.dispatch(finished(rows = 2))
        toasts.noToast("a push past the wait belongs to another sync")
        assertNull(s.deviceSyncWait.get())
    }

    @Test fun aFullSyncThatBroughtOnlyContactsDoesNotSayNothingNew() {
        
        
        val d = FakeDaemon()
        val s = store(d)
        val toasts = toastsOf(s)
        startedSync(s, d, toasts, full = true)
        s.dispatch(finished(full = true, snapshot = 4, held = 2))
        assertEquals("Full sync finished: 4 changes, 2 not placed yet", toasts.nextToast())
    }

    @Test fun theDaemonsStoppingDropsTheWait() {
        val d = FakeDaemon()
        val s = store(d)
        val toasts = toastsOf(s)
        startedSync(s, d, toasts)
        s.onDaemonsStopped()
        assertNull(s.deviceSyncWait.get())
        s.dispatch(finished(rows = 1))
        toasts.noToast("a push after the daemons stopped belongs to no sync of this phone")
    }

    @Test fun lockingDropsTheWait() {
        val d = FakeDaemon()
        val s = store(d)
        val toasts = toastsOf(s)
        startedSync(s, d, toasts)
        s.syncLockState(true)
        assertNull(s.deviceSyncWait.get())
        s.syncLockState(false)
        s.dispatch(finished(rows = 1))
        toasts.noToast("a sync started before the lock shows no end after it")
    }

    @Test fun theWaitIsTheDaemonsCapAndAMargin() {
        
        
        assertTrue(InheritedPairingTimings().syncFinishWait.inWholeSeconds > 600)
    }

    @Test fun theFinishWords() {
        assertEquals("Sync finished: 1 change", deviceSyncFinishedToast(DeviceSyncFinishedPush(false, 1, 1, 1, false)))
        assertEquals(
            "Sync stopped waiting: no answer from the linked device",
            deviceSyncFinishedToast(DeviceSyncFinishedPush(false, 1, 0, 0, true)),
        )
        assertEquals(
            "Sync stopped waiting: 1 of 2 linked devices answered, 4 changes",
            deviceSyncFinishedToast(DeviceSyncFinishedPush(true, 2, 1, 4, true)),
        )
        assertEquals(
            "Sync finished: 5 changes",
            deviceSyncFinishedToast(DeviceSyncFinishedPush(false, 1, 1, 3, false, snapshotApplied = 2)),
        )
        assertEquals(
            "Sync failed: could not reach the linked device",
            deviceSyncFinishedToast(DeviceSyncFinishedPush(false, 1, 0, 0, false, unreachable = 1)),
        )
        assertEquals(
            "Sync finished: 1 of 2 linked devices answered, 1 change",
            deviceSyncFinishedToast(DeviceSyncFinishedPush(false, 2, 1, 0, false, snapshotApplied = 1, unreachable = 1)),
        )
        for (p in listOf(
            DeviceSyncFinishedPush(false, 1, 1, 0, false),
            DeviceSyncFinishedPush(true, 2, 0, 0, true),
            DeviceSyncFinishedPush(false, 1, 0, 0, false, unreachable = 1),
        )) {
            assertFalse("never \"synced\"", deviceSyncFinishedToast(p).contains("synced"))
        }
    }
}
