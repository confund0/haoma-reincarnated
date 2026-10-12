package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.Frame
import io.haoma.calculator.core.ipc.FrameType
import io.haoma.calculator.messenger.contacts.detailColour
import java.util.Collections
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicLong
import kotlin.time.Duration
import kotlin.time.Duration.Companion.seconds
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test


class InheritedPairingTest {

    
    private class FakeDaemon {
        val sent: MutableList<Pair<String, JSONObject?>> = Collections.synchronizedList(mutableListOf())
        val answers = mutableMapOf<String, Frame>()
        val pending = LinkedBlockingQueue<Pending>()
        val inFlight = AtomicInteger(0)
        val mostInFlight = AtomicInteger(0)
        val timeouts: MutableList<Pair<String, Duration>> = Collections.synchronizedList(mutableListOf())

        class Pending(val type: String, val payload: JSONObject?, val reply: CompletableDeferred<Frame>)

        fun countOf(type: String): Int = synchronized(sent) { sent.count { it.first == type } }

        fun offersTo(fp: String): Int = synchronized(sent) {
            sent.count { it.first == FrameType.InvitationOffer && it.second?.optString("fingerprint") == fp }
        }

        fun timeoutOf(type: String): Duration? = synchronized(timeouts) { timeouts.lastOrNull { it.first == type }?.second }

        fun typesSent(): List<String> = synchronized(sent) { sent.map { it.first } }

        suspend fun handle(type: String, payload: JSONObject?, timeout: Duration): Frame {
            timeouts.add(type to timeout)
            sent.add(type to payload)
            if (type == FrameType.InvitationOffer || type == FrameType.InvitationAccept) {
                val now = inFlight.incrementAndGet()
                mostInFlight.accumulateAndGet(now) { a, b -> maxOf(a, b) }
                val d = CompletableDeferred<Frame>()
                pending.add(Pending(type, payload, d))
                try {
                    return d.await()
                } finally {
                    inFlight.decrementAndGet()
                }
            }
            return synchronized(answers) { answers[type] } ?: Frame(type = FrameType.Error)
        }

        
        fun next(): Pending =
            pending.poll(5, TimeUnit.SECONDS) ?: throw AssertionError("no offer or accept reached the daemon")

        
        fun nothingMoreWaiting(what: String) {
            val extra = pending.poll(300, TimeUnit.MILLISECONDS)
            assertNull(what, extra)
        }
    }

    
    private val clock = AtomicLong(NOW)

    private fun store(daemon: FakeDaemon, connected: Boolean = true) = MessengerStore(
        timedRequestSender = { type, payload, timeout -> daemon.handle(type, payload, timeout) },
        inheritedPairingTimings = InheritedPairingTimings(
            nowSeconds = { clock.get() },
            lostOfferRecheckMs = 10L,
        ),
    ).also { it._connection.value = connected }

    
    private fun contactsJson(linked: Boolean, vararg fps: String) = JSONObject()
        .put(
            "contacts",
            JSONArray().apply {
                fps.forEach { put(JSONObject().put("fingerprint", it).put("label", "name-$it")) }
            },
        )
        .put("linked", linked)

    private fun listed(linked: Boolean, vararg fps: String) =
        Frame(type = FrameType.InheritedContactsListed, payload = contactsJson(linked, *fps))

    private fun changed(linked: Boolean, vararg fps: String) =
        Frame(type = FrameType.InheritedContactsChanged, payload = contactsJson(linked, *fps))

    private fun offered(siblings: Int = 1) = Frame(
        type = FrameType.InvitationOffered,
        payload = JSONObject().put("msg_id", "m").put("expires_at", NOW + 600).put("siblings", siblings),
    )

    private fun refused(code: String) = Frame(
        type = FrameType.Error,
        payload = JSONObject().put("code", code).put("message", "tor said onion abcdef.onion is gone"),
    )

    private fun invitation(
        msgId: String,
        direction: String,
        expiresAt: Long,
        state: String = InvitationState.LIVE,
        fingerprint: String = "",
        label: String = "",
    ) = JSONObject()
        .put("msg_id", msgId)
        .put("label", label)
        .put("direction", direction)
        .put("expires_at", expiresAt)
        .put("state", state)
        .apply { if (fingerprint.isNotEmpty()) put("fingerprint", fingerprint) }

    private fun invitationsListed(vararg entries: JSONObject) = Frame(
        type = FrameType.InvitationsListed,
        payload = JSONObject().put("invitations", JSONArray().apply { entries.forEach { put(it) } }),
    )

    
    private val MessengerStore.pairing: InheritedPairing get() = _inheritedPairing.value
    private fun MessengerStore.stateOf(fp: String) = pairing.stateOf(fp, clock.get())
    private fun MessengerStore.detailsOf(fp: String) = pairing.detailsOf(fp, clock.get())
    private fun MessengerStore.pairAllCandidates() = pairing.pairAllCandidates(clock.get())

    
    private fun awaitThat(what: String, cond: () -> Boolean) {
        val deadline = System.currentTimeMillis() + 5_000L
        while (System.currentTimeMillis() < deadline) {
            if (cond()) return
            Thread.sleep(10)
        }
        throw AssertionError(what)
    }

    
    private fun holding(daemon: FakeDaemon, vararg fps: String, connected: Boolean = true): MessengerStore {
        val s = store(daemon, connected)
        s.dispatch(changed(true, *fps))
        return s
    }

    
    @Test
    fun thePushAndTheListAnswerApplyTheSameWay() {
        val before = InheritedPairing(
            contacts = listOf(InheritedContactEntry("a"), InheritedContactEntry("b"), InheritedContactEntry("c")),
            linked = true,
            states = mapOf("b" to PairState.OFFERED, "c" to PairState.FAILED),
            details = mapOf("b" to listOf("x"), "c" to listOf("y")),
        )

        val daemon = FakeDaemon()
        daemon.answers[FrameType.ListInheritedContacts] = listed(true, "a", "b")
        val asked = store(daemon)
        asked._inheritedPairing.value = before
        runBlocking { asked.refreshInheritedContacts() }

        val pushed = store(FakeDaemon())
        pushed._inheritedPairing.value = before
        pushed.dispatch(changed(true, "a", "b"))

        assertEquals("the push and the list answer left the section in different states", asked.pairing, pushed.pairing)
        assertEquals(listOf("a", "b"), pushed.pairing.contacts.map { it.fingerprint })
        assertTrue(pushed.pairing.linked)
        assertEquals("the contact who stayed kept what the run knew", PairState.OFFERED, pushed.stateOf("b"))
        assertFalse("a contact who left the list kept a run state", "c" in pushed.pairing.states)
        assertFalse("a contact who left the list kept its lines", "c" in pushed.pairing.details)
    }

    
    @Test
    fun aListAnswerThatAPushOvertookIsDropped() {
        val release = CompletableDeferred<Unit>()
        val s = MessengerStore(
            requestSender = { type, _ ->
                if (type == FrameType.ListInheritedContacts) {
                    release.await()
                    listed(true, "old")
                } else {
                    Frame(type = FrameType.Error)
                }
            },
        ).also { it._connection.value = true }

        runBlocking {
            val job = s.scope.launch { s.refreshInheritedContacts() }
            awaitThat("the list request never went out") { s.inheritedListSeq.get() == 1L }
            s.dispatch(changed(true, "new"))
            release.complete(Unit)
            job.join()
        }
        assertEquals(listOf("new"), s.pairing.contacts.map { it.fingerprint })
    }

    
    @Test
    fun aListThatFailedIsNotSynchronized() {
        val daemon = FakeDaemon()
        daemon.answers[FrameType.ListInheritedContacts] = refused("store_read_failed")
        val s = holding(daemon, "a")
        runBlocking { s.refreshInheritedContacts() }
        assertEquals("The contacts with no pairing could not be read.", s.pairing.listFailure.first())
        assertEquals("a failed read dropped the contacts already shown", listOf("a"), s.pairing.contacts.map { it.fingerprint })
    }

    
    @Test
    fun oneOfferInFlightAndTheNextAfterTheAnswer() {
        val daemon = FakeDaemon()
        val s = holding(daemon, "a", "b", "c")

        s.pairInherited("a")
        s.pairInherited("b")
        s.pairInherited("c")

        val first = daemon.next()
        assertEquals("a", first.payload?.optString("fingerprint"))
        daemon.nothingMoreWaiting("a second offer went out while the first was unanswered")
        assertEquals(PairState.OFFERING, s.stateOf("a"))
        assertEquals(PairState.WAITING, s.stateOf("b"))
        assertEquals(PairState.WAITING, s.stateOf("c"))

        
        s.pairInherited("b")
        assertEquals(PAIRING_ALREADY, s.pairing.note)

        first.reply.complete(offered(siblings = 2))
        val second = daemon.next()
        assertEquals("b", second.payload?.optString("fingerprint"))
        assertEquals(PairState.OFFERED, s.stateOf("a"))
        assertEquals(listOf(handedTo(2)), s.detailsOf("a"))
        daemon.nothingMoreWaiting("a third offer went out while the second was unanswered")

        second.reply.complete(refused("rail_send_failed"))
        val third = daemon.next()
        assertEquals("c", third.payload?.optString("fingerprint"))
        assertEquals("a refusal is not a reason to leave the rest unstarted", PairState.FAILED, s.stateOf("b"))
        third.reply.complete(offered())

        awaitThat("the run did not end") { !s.pairing.running }
        assertEquals("more than one offer was out at once", 1, daemon.mostInFlight.get())
        assertEquals(3, daemon.countOf(FrameType.InvitationOffer))
        assertEquals(1, daemon.offersTo("b"))
    }

    
    @Test
    fun aRunStartedWhileDisconnectedHoldsAndResumes() {
        val daemon = FakeDaemon()
        val s = holding(daemon, "a", connected = false)
        s.pairInherited("a")

        awaitThat("the run did not hold") { !s.pairing.running }
        assertEquals(0, daemon.countOf(FrameType.InvitationOffer))
        assertEquals(PairState.WAITING, s.stateOf("a"))
        assertEquals(pairingHeld(1), s.pairing.note)

        s._connection.value = true
        s.resumeInheritedPairing()
        val offer = daemon.next()
        assertEquals("a", offer.payload?.optString("fingerprint"))
        assertEquals(pairingResumed(1), s.pairing.note)
        offer.reply.complete(offered())
        awaitThat("the resumed run did not record the answer") { s.stateOf("a") == PairState.OFFERED }
    }

    
    @Test
    fun aLostAnswerIsNoAnswerAndIsNotOfferedAgain() {
        val daemon = FakeDaemon()
        val s = holding(daemon, "a", "b")
        s.pairInherited("a")
        s.pairInherited("b")

        val first = daemon.next()
        assertEquals("a", first.payload?.optString("fingerprint"))
        s._connection.value = false

        awaitThat("the lost answer was not recorded") { s.stateOf("a") == PairState.UNKNOWN }
        assertEquals("The invitation had no answer.", s.detailsOf("a").first())
        awaitThat("the run did not hold") { !s.pairing.running }
        assertEquals(PairState.WAITING, s.stateOf("b"))
        daemon.nothingMoreWaiting("an offer went out into a connection that is down")

        
        clock.set(NOW + 331)
        s._connection.value = true
        s.resumeInheritedPairing()
        val second = daemon.next()
        assertEquals("b", second.payload?.optString("fingerprint"))
        second.reply.complete(offered())
        awaitThat("the run did not end") { !s.pairing.running }

        assertEquals("the contact whose answer was lost was offered again", 1, daemon.offersTo("a"))
        assertEquals(PairState.UNKNOWN, s.stateOf("a"))
        assertFalse("pair all would offer again to a contact with no answer", "a" in s.pairAllCandidates())

        
        first.reply.complete(offered())
        Thread.sleep(100)
        assertEquals("a late answer on a dead session changed the row", PairState.UNKNOWN, s.stateOf("a"))
        assertEquals(1, daemon.offersTo("a"))
        daemon.nothingMoreWaiting("a late answer started another offer")
    }

    
    @Test
    fun stopDropsTheWaitingAndLeavesTheOfferInFlight() {
        val daemon = FakeDaemon()
        val s = holding(daemon, "a", "b", "c")
        s.pairInherited("a")
        s.pairInherited("b")
        s.pairInherited("c")
        val first = daemon.next()

        s.stopInheritedPairing()
        assertEquals(pairingStopped(2), s.pairing.note)
        assertEquals("", s.stateOf("b"))
        assertEquals(PairState.OFFERING, s.stateOf("a"))

        first.reply.complete(offered())
        awaitThat("the run did not end") { !s.pairing.running }
        daemon.nothingMoreWaiting("a stopped contact was offered")
        assertEquals(PairState.OFFERED, s.stateOf("a"))
    }

    
    @Test
    fun aContactWhoPairedWhileTheOfferWasOutLeavesNoState() {
        val daemon = FakeDaemon()
        val s = holding(daemon, "a", "b")
        s.pairInherited("a")
        s.pairInherited("b")
        val first = daemon.next()

        s.dispatch(changed(true, "a"))
        first.reply.complete(offered())
        awaitThat("the run did not end") { !s.pairing.running }

        daemon.nothingMoreWaiting("a contact who left the list was still offered")
        assertEquals(0, daemon.offersTo("b"))
        assertFalse("b" in s.pairing.states)
    }

    
    @Test
    fun anUnlinkDropsTheQueue() {
        val daemon = FakeDaemon()
        val s = holding(daemon, "a", "b")
        s.pairInherited("a")
        s.pairInherited("b")
        val first = daemon.next()

        s.dispatch(changed(false, "a", "b"))
        assertFalse(s.pairing.linked)
        assertTrue(s.pairing.queue.isEmpty())
        assertEquals(pairingStopped(1), s.pairing.note)

        first.reply.complete(offered())
        awaitThat("the run did not end") { !s.pairing.running }
        assertEquals(0, daemon.offersTo("b"))
    }

    
    @Test
    fun pairAllSkipsEveryStandingInvitationIncludingOnesFromTheList() {
        val daemon = FakeDaemon()
        daemon.answers[FrameType.InvitationsList] = invitationsListed(
            invitation("m-d", InvitationDirection.OUT, NOW + 3600, fingerprint = "d"),
            invitation("m-e", InvitationDirection.OUT, NOW - 1, fingerprint = "e"),
            invitation("m-f", InvitationDirection.OUT, NOW + 3600, state = InvitationState.SUPERSEDED, fingerprint = "f"),
            invitation("m-g", InvitationDirection.IN, NOW + 3600),
        )
        val s = holding(daemon, "a", "b", "c", "d", "e", "f")
        s._inheritedPairing.value = s.pairing.copy(
            states = mapOf("a" to PairState.OFFERED, "b" to PairState.UNKNOWN, "c" to PairState.FAILED),
        )
        runBlocking { s.refreshInvitations() }

        assertEquals("the list did not rebuild invitation sent", PairState.OFFERED, s.stateOf("d"))
        assertEquals("a lapsed invitation does not stand", "", s.stateOf("e"))
        assertEquals("a retired invitation does not stand", "", s.stateOf("f"))
        assertEquals(listOf("c", "e", "f"), s.pairAllCandidates())
        assertEquals(3, s.pairing.leftAloneByPairAll(clock.get()))

        s.pairAllInherited()
        val offeredTo = mutableListOf<String>()
        repeat(3) {
            val p = daemon.next()
            offeredTo += p.payload!!.getString("fingerprint")
            p.reply.complete(offered())
        }
        awaitThat("the run did not end") { !s.pairing.running }
        daemon.nothingMoreWaiting("pair all offered to a contact with a standing invitation")
        assertEquals(listOf("c", "e", "f"), offeredTo)
        for (fp in listOf("a", "b", "d")) {
            assertEquals("pair all offered again to $fp, whose invitation may stand", 0, daemon.offersTo(fp))
        }
    }

    
    @Test
    fun aStandingInvitationTurnsNoAnswerIntoInvitationSent() {
        val daemon = FakeDaemon()
        daemon.answers[FrameType.InvitationsList] = invitationsListed(
            invitation("m-a", InvitationDirection.OUT, NOW + 3600, fingerprint = "a"),
        )
        val s = holding(daemon, "a", "b")
        s._inheritedPairing.value = s.pairing.copy(
            states = mapOf("a" to PairState.UNKNOWN, "b" to PairState.FAILED),
            details = mapOf("a" to listOf("The invitation had no answer.")),
        )
        runBlocking { s.refreshInvitations() }
        assertEquals(PairState.OFFERED, s.stateOf("a"))
        assertEquals("the no-answer lines stayed under invitation sent", emptyList<String>(), s.detailsOf("a"))
        assertEquals("a refusal with nothing standing stayed a refusal", PairState.FAILED, s.stateOf("b"))
    }

    
    @Test
    fun anUnframedSuccessIsAnInvitationSent() {
        val o = offerOutcome(refused("encode_frame"))
        assertEquals(PairState.OFFERED, o.state)
        assertEquals(listOf(OFFERED_UNREADABLE), o.lines)
        assertEquals(PairState.OFFERED, offerOutcome(Frame(type = FrameType.InvitationOffered)).state)
    }


    @Test
    fun invitationSentGoesWhenANewerListShowsNothingStanding() {
        val daemon = FakeDaemon()
        daemon.answers[FrameType.InvitationsList] = invitationsListed()
        val s = holding(daemon, "a")
        s.pairInherited("a")
        daemon.next().reply.complete(offered())
        awaitThat("the run did not end") { !s.pairing.running }
        awaitThat("a newer list with nothing standing left invitation sent on the row") { s.stateOf("a") == "" }
        assertTrue("pair all would not offer again after the invitation was gone", "a" in s.pairAllCandidates())
    }

    
    @Test
    fun onlyAListAskedForAfterTheAnswerTakesInvitationSentBack() {
        val a = listOf(InheritedContactEntry("a"))
        val sent = InheritedPairing(
            contacts = a,
            linked = true,
            states = mapOf("a" to PairState.OFFERED),
            outcomeListSeq = mapOf("a" to 5L),
            offeredUntil = mapOf("a" to NOW + 600),
            appliedListSeq = 5L,
        )
        assertEquals("a list asked for before the answer took it back", PairState.OFFERED, sent.stateOf("a", NOW))
        assertEquals("", sent.copy(appliedListSeq = 6L).stateOf("a", NOW))
        assertEquals(
            PairState.OFFERED,
            sent.copy(appliedListSeq = 6L, standingOut = mapOf("a" to NOW + 600)).stateOf("a", NOW),
        )
        assertEquals("an invitation past its deadline still read as sent", "", sent.stateOf("a", NOW + 600))

        val fromList = InheritedPairing(contacts = a, linked = true, standingOut = mapOf("a" to NOW + 10))
        assertEquals(PairState.OFFERED, fromList.stateOf("a", NOW))
        assertEquals("a lapsed invitation from the list still read as sent", "", fromList.stateOf("a", NOW + 10))

        val lost = InheritedPairing(
            contacts = a,
            linked = true,
            states = mapOf("a" to PairState.UNKNOWN),
            outcomeListSeq = mapOf("a" to 5L),
            appliedListSeq = 9L,
        )
        assertEquals("no answer came back must stay", PairState.UNKNOWN, lost.stateOf("a", NOW))
    }

    
    @Test
    fun aRefusalWithAnEarlierInvitationStandingReadsAsSent() {
        val daemon = FakeDaemon()
        daemon.answers[FrameType.InvitationsList] = invitationsListed(
            invitation("m-a", InvitationDirection.OUT, NOW + 3600, fingerprint = "a"),
        )
        val s = holding(daemon, "a")
        s.pairInherited("a")
        daemon.next().reply.complete(refused("invite_failed"))
        awaitThat("the refusal hid the standing invitation") { s.stateOf("a") == PairState.OFFERED }
        assertFalse("pair all would retire the standing invitation", "a" in s.pairAllCandidates())
        assertEquals("the refusal's lines stayed under invitation sent", emptyList<String>(), s.detailsOf("a"))
    }

    @Test
    fun theKeyStashRefusalDoesNotSayNothingStands() {
        for (t in listOf("key_stash_failed", "tag_failed", "invite_failed", "build_failed", "backend_unavailable")) {
            val lines = explainOfferRefusal(t).lines
            assertFalse("$t says nothing was left standing", lines.any { "Nothing was left standing" in it })
            assertTrue("$t does not say an earlier invitation may stand", lines.any { "may still stand" in it })
        }
    }

    
    @Test
    fun anOfferInProgressRefusalIsDidNotGoOutAndPairAllOffersAgain() {
        val o = offerOutcome(refused("offer_in_progress"))
        assertEquals(PairState.FAILED, o.state)
        assertEquals("Another invitation is being offered right now.", o.lines.first())

        val daemon = FakeDaemon()
        val s = holding(daemon, "a")
        s.pairInherited("a")
        daemon.next().reply.complete(refused("offer_in_progress"))
        awaitThat("the run did not end") { !s.pairing.running }
        assertEquals(PairState.FAILED, s.stateOf("a"))
        assertTrue("pair all would not offer again after offer_in_progress", "a" in s.pairAllCandidates())
    }

    
    private fun lostThenBack(daemon: FakeDaemon): MessengerStore {
        daemon.answers[FrameType.InvitationsList] = invitationsListed()
        val s = holding(daemon, "a", "b")
        s.pairInherited("a")
        s.pairInherited("b")
        assertNotNull(daemon.next())
        s._connection.value = false
        awaitThat("the run did not hold") { !s.pairing.running && s.stateOf("a") == PairState.UNKNOWN }
        s._connection.value = true
        s.resumeInheritedPairing()
        return s
    }

    
    @Test
    fun theNextOfferWaitsUntilTheLostOneShowsInTheList() {
        val daemon = FakeDaemon()
        val s = lostThenBack(daemon)

        daemon.nothingMoreWaiting("the next offer went out while the lost one may still be running")
        assertTrue(s.pairing.note.startsWith("the next offer waits"))
        assertTrue("the wait did not read the list again", daemon.countOf(FrameType.InvitationsList) >= 2)

        synchronized(daemon.answers) {
            daemon.answers[FrameType.InvitationsList] = invitationsListed(
                invitation("m-a", InvitationDirection.OUT, NOW + 3600, fingerprint = "a"),
            )
        }
        val next = daemon.next()
        assertEquals("b", next.payload?.optString("fingerprint"))
        assertEquals(PairState.OFFERED, s.stateOf("a"))
        next.reply.complete(offered())
    }

    
    @Test
    fun theNextOfferGoesWhenTheLostOnesTimeoutHasPassed() {
        val daemon = FakeDaemon()
        val s = lostThenBack(daemon)
        daemon.nothingMoreWaiting("the next offer went out before the lost one's timeout")

        clock.set(NOW + 330)
        val next = daemon.next()
        assertEquals("b", next.payload?.optString("fingerprint"))
        assertEquals("the lost offer lost its state", PairState.UNKNOWN, s.stateOf("a"))
        next.reply.complete(offered())
    }

    
    @Test
    fun theNoteGoesWhenTheRunEnds() {
        val daemon = FakeDaemon()
        val s = holding(daemon, "a")
        s.pairInherited("a")
        assertEquals(pairingStarted(1), s.pairing.note)
        daemon.next().reply.complete(offered())
        awaitThat("the run did not end") { !s.pairing.running }
        assertEquals("the note still says the run is going", "", s.pairing.note)
    }

    
    @Test
    fun aCancelledRunPutsItsFlagDownAndLeavesNoOfferingRow() {
        val daemon = FakeDaemon()
        val s = holding(daemon, "a", "b")
        s.pairInherited("a")
        assertNotNull(daemon.next())

        s.scope.coroutineContext[kotlinx.coroutines.Job]!!.children.forEach { it.cancel() }
        awaitThat("a cancelled run left its flag up") { !s.pairing.running }
        assertEquals(PairState.UNKNOWN, s.stateOf("a"))

        clock.set(NOW + 331)
        s.pairInherited("b")
        val next = daemon.next()
        assertEquals("no run could start after the cancelled one", "b", next.payload?.optString("fingerprint"))
        next.reply.complete(offered())
    }

    
    private fun row(
        fp: String,
        devices: Int = 0,
        paired: Int = 0,
        offers: Int = 0,
        waiting: Boolean = false,
        invited: Boolean = false,
        open: Int = 0,
    ) = JSONObject().put("fingerprint", fp).put("label", "name-$fp").apply {
        if (devices != 0) put("devices", devices)
        if (paired != 0) put("paired", paired)
        if (offers != 0) put("offers", offers)
        if (waiting) put("waiting", true)
        if (invited) put("invited", true)
        if (open != 0) put("open", open)
    }

    private fun changedRows(vararg rows: JSONObject) = Frame(
        type = FrameType.InheritedContactsChanged,
        payload = JSONObject().put("contacts", JSONArray().apply { rows.forEach { put(it) } }).put("linked", true),
    )

    @Test
    fun theCountsAreParsedAndDefaultToZero() {
        val full = InheritedContactEntry.fromJson(row("a", devices = 2, paired = 1, offers = 1, waiting = true, invited = true, open = 1))
        assertEquals(1, full.open)
        assertEquals(2, full.devices)
        assertEquals(1, full.paired)
        assertEquals(1, full.offers)
        assertTrue(full.waiting)
        assertTrue(full.invited)

        
        val bare = InheritedContactEntry.fromJson(JSONObject().put("fingerprint", "b"))
        assertEquals(InheritedContactEntry("b"), bare)
        assertEquals(0, bare.devices)
        assertEquals(0, bare.paired)
        assertEquals(0, bare.offers)
        assertFalse(bare.waiting)
        assertFalse(bare.invited)
        assertEquals(0, bare.open)
    }

    
    private fun offeredOnceTo(daemon: FakeDaemon, s: MessengerStore) {
        s.dispatch(changedRows(row("a", devices = 1, offers = 1, open = 1)))
        s.pairInherited("a")
        daemon.next().reply.complete(offered())
        awaitThat("the run did not end") { !s.pairing.running }
    }

    
    @Test
    fun aPushWithAnOpenDeviceOffersItAgain() {
        val daemon = FakeDaemon()
        val s = store(daemon)
        offeredOnceTo(daemon, s)

        s.dispatch(changedRows(row("a", devices = 2, offers = 2, open = 1)))
        val again = daemon.next()
        assertEquals("a", again.payload?.optString("fingerprint"))
        assertTrue("the offer queued again went without force", again.payload!!.optBoolean("force"))
        assertTrue("the offer queued again went without open_only", again.payload!!.optBoolean("open_only"))
        daemon.nothingMoreWaiting("a push with open=1 made more than one offer")
        again.reply.complete(offered())
        awaitThat("the run did not end") { !s.pairing.running }
        assertEquals(2, daemon.offersTo("a"))

        
        s.dispatch(changedRows(row("a", devices = 2, offers = 1)))
        daemon.nothingMoreWaiting("a push with open=0 made an offer")
        assertEquals(2, daemon.offersTo("a"))
    }

    
    @Test
    fun aPushQueuesNothingElse() {
        val cases = listOf(
            "waiting" to row("a", devices = 2, offers = 1, open = 1, waiting = true),
            "invited" to row("a", devices = 2, offers = 1, open = 1, invited = true),
            "open=0" to row("a", devices = 2, offers = 1),
        )
        for ((what, pushed) in cases) {
            val daemon = FakeDaemon()
            val s = store(daemon)
            offeredOnceTo(daemon, s)
            s.dispatch(changedRows(pushed))
            daemon.nothingMoreWaiting("a push queued the $what row again")
            assertEquals(what, 1, daemon.offersTo("a"))
            assertTrue(what, s.pairing.queue.isEmpty())
        }

        
        val daemon = FakeDaemon()
        val s = store(daemon)
        s.dispatch(changedRows(row("b", devices = 2, offers = 2, open = 2)))
        daemon.nothingMoreWaiting("a push offered to a contact never offered in this session")
        assertTrue(s.pairing.queue.isEmpty())

        
        val listed = store(daemon)
        offeredOnceTo(daemon, listed)
        synchronized(daemon.answers) {
            daemon.answers[FrameType.ListInheritedContacts] = Frame(
                type = FrameType.InheritedContactsListed,
                payload = JSONObject().put("contacts", JSONArray().put(row("a", devices = 2, offers = 2, open = 1))).put("linked", true),
            )
        }
        runBlocking { listed.refreshInheritedContacts() }
        daemon.nothingMoreWaiting("a list answer queued a contact again")
        assertTrue(listed.pairing.queue.isEmpty())
    }

    
    @Test
    fun aPushDuringAnOfferIsCheckedAgainOnAFreshList() {
        val daemon = FakeDaemon()
        val s = store(daemon)
        s.dispatch(changedRows(row("a", devices = 1, offers = 1, open = 1)))
        s.pairInherited("a")
        val first = daemon.next()
        assertEquals(PairState.OFFERING, s.stateOf("a"))

        
        s.dispatch(changedRows(row("a", devices = 2, offers = 2, open = 2)))
        assertTrue("a push queued a contact whose offer is out", s.pairing.queue.isEmpty())
        assertEquals(setOf("a"), s.pairing.pushedDuringOffer)

        synchronized(daemon.answers) {
            daemon.answers[FrameType.ListInheritedContacts] = Frame(
                type = FrameType.InheritedContactsListed,
                payload = JSONObject().put("contacts", JSONArray().put(row("a", devices = 2, offers = 2, open = 1))).put("linked", true),
            )
        }
        first.reply.complete(offered())
        val again = daemon.next()
        assertEquals("a", again.payload?.optString("fingerprint"))
        assertTrue(again.payload!!.optBoolean("force"))
        assertTrue("the offer queued after the fresh list went without open_only", again.payload!!.optBoolean("open_only"))
        assertEquals("the fresh list was not read once", 1, daemon.countOf(FrameType.ListInheritedContacts))
        daemon.nothingMoreWaiting("the fresh list made more than one offer")
        assertTrue(s.pairing.pushedDuringOffer.isEmpty())
        again.reply.complete(offered())
        awaitThat("the run did not end") { !s.pairing.running }
        assertEquals(2, daemon.offersTo("a"))
        assertEquals("a list was read after an offer with no push during it", 1, daemon.countOf(FrameType.ListInheritedContacts))

        
        val quiet = FakeDaemon()
        val q = store(quiet)
        offeredOnceTo(quiet, q)
        quiet.nothingMoreWaiting("an offer with no push during it made another")
        assertEquals(0, quiet.countOf(FrameType.ListInheritedContacts))
        assertEquals(1, quiet.offersTo("a"))
    }

    
    @Test
    fun nothingOpenIsDoneAndTheRunGoesOn() {
        val daemon = FakeDaemon()
        val s = store(daemon)
        offeredOnceTo(daemon, s)

        s.dispatch(changedRows(row("a", devices = 3, offers = 3, open = 2)))
        val first = daemon.next()
        assertTrue(first.payload!!.optBoolean("open_only"))
        first.reply.complete(refused("nothing_open"))
        val second = daemon.next()
        assertEquals("the run did not go on after nothing_open", "a", second.payload?.optString("fingerprint"))
        assertTrue("the second copy lost its open-only mark", second.payload!!.optBoolean("open_only"))
        second.reply.complete(refused("nothing_open"))
        awaitThat("the run did not end") { !s.pairing.running }

        assertEquals(PairState.OFFERED, s.stateOf("a"))
        assertTrue("nothing_open left a line under the row", s.detailsOf("a").isEmpty())
        assertNull(s.pairing.lostOffer)
        assertTrue("the mark outlived the last copy", s.pairing.openOnly.isEmpty())
    }

    
    @Test
    fun aPressAndStopClearTheOpenOnlyMark() {
        val base = InheritedPairing(
            contacts = listOf(InheritedContactEntry("a", offers = 1, open = 1)),
            linked = true,
            offeredThisSession = setOf("a"),
        )
        val requeued = base.requeueOffered(NOW).first
        assertEquals(setOf("a"), requeued.openOnly)
        assertTrue(requeued.stopped().first.openOnly.isEmpty())

        val pressed = base.copy(openOnly = setOf("a")).enqueue(listOf("a")).first
        assertEquals(listOf("a"), pressed.queue)
        assertTrue("a press kept the open-only mark", pressed.openOnly.isEmpty())
    }

    @Test
    fun theOpenOnlyFlagIsSentOnlyWhenTrue() {
        assertFalse(InvitationOfferRequest("a", force = true).toJson().has("open_only"))
        assertTrue(InvitationOfferRequest("a", force = true, openOnly = true).toJson().getBoolean("open_only"))
        assertEquals(
            listOf("An invitation from this device already stands for each device of that person.", "Nothing new went out, and nothing was taken back."),
            explainOfferRefusal("nothing_open").lines,
        )
    }

    
    @Test
    fun aStoppedContactIsNotQueuedAgainByAPush() {
        val p = InheritedPairing(
            contacts = listOf(InheritedContactEntry("a", offers = 2, open = 1)),
            linked = true,
            states = mapOf("a" to PairState.WAITING),
            queue = listOf("a"),
            offeredThisSession = setOf("a"),
        )
        val (stopped, _) = p.stopped()
        assertEquals(0, stopped.requeueOffered(NOW).second)
        val idle = p.copy(queue = emptyList(), states = emptyMap())
        assertEquals(1, idle.requeueOffered(NOW).second)
        assertEquals(1, idle.copy(states = mapOf("a" to PairState.OFFERED)).requeueOffered(NOW).second)
        
        assertEquals("FAILED was queued again", 0, idle.copy(states = mapOf("a" to PairState.FAILED)).requeueOffered(NOW).second)
        assertEquals("UNKNOWN was queued again", 0, idle.copy(states = mapOf("a" to PairState.UNKNOWN)).requeueOffered(NOW).second)
    }

    
    @Test
    fun aContactMissingTwoDevicesIsOfferedTwice() {
        val daemon = FakeDaemon()
        val s = store(daemon)
        s.dispatch(changedRows(row("a", offers = 2)))

        val (queued, joined) = s.pairing.enqueue(listOf("a"))
        assertEquals("one contact joined, not two", 1, joined)
        assertEquals(listOf("a", "a"), queued.queue)
        assertEquals(PairState.WAITING, queued.states["a"])

        s.pairInherited("a")
        assertEquals(pairingStarted(1), s.pairing.note)
        val first = daemon.next()
        assertEquals("a", first.payload?.optString("fingerprint"))
        assertTrue("an offer of the run went without force", first.payload!!.optBoolean("force"))
        assertFalse("a pressed offer went open-only", first.payload!!.has("open_only"))
        assertEquals(PairState.OFFERING, s.stateOf("a"))
        assertEquals(listOf("a"), s.pairing.queue)
        daemon.nothingMoreWaiting("the second offer went out while the first was unanswered")

        
        s.pairInherited("a")
        assertEquals(PAIRING_ALREADY, s.pairing.note)

        first.reply.complete(offered())
        val second = daemon.next()
        assertEquals("a", second.payload?.optString("fingerprint"))
        assertTrue("an offer of the run went without force", second.payload!!.optBoolean("force"))
        assertEquals(PairState.OFFERING, s.stateOf("a"))
        assertTrue(s.pairing.queue.isEmpty())
        second.reply.complete(offered())

        awaitThat("the run did not end") { !s.pairing.running }
        assertEquals(PairState.OFFERED, s.stateOf("a"))
        assertEquals(2, daemon.offersTo("a"))
        assertEquals("more than one offer was out at once", 1, daemon.mostInFlight.get())
    }

    
    @Test
    fun aStandingContactIsQueuedOnce() {
        for (st in listOf(PairState.OFFERED, PairState.UNKNOWN)) {
            val p = InheritedPairing(
                contacts = listOf(InheritedContactEntry("a", offers = 2)),
                linked = true,
                states = mapOf("a" to st),
            )
            assertEquals("state $st", listOf("a"), p.enqueue(listOf("a")).first.queue)
        }
        val refused = InheritedPairing(
            contacts = listOf(InheritedContactEntry("a", offers = 2)),
            linked = true,
            states = mapOf("a" to PairState.FAILED),
        )
        assertEquals(listOf("a", "a"), refused.enqueue(listOf("a")).first.queue)
        
        val old = InheritedPairing(contacts = listOf(InheritedContactEntry("b")), linked = true)
        assertEquals(listOf("b"), old.enqueue(listOf("b")).first.queue)
    }

    
    @Test
    fun aWaitingRowIsNeitherQueuedNorAPairAllCandidate() {
        val daemon = FakeDaemon()
        val s = store(daemon)
        s.dispatch(changedRows(row("a", devices = 2, paired = 1, waiting = true), row("b", offers = 1)))

        val (next, joined) = s.pairing.enqueue(listOf("a"))
        assertEquals(0, joined)
        assertTrue(next.queue.isEmpty())
        assertNull(next.states["a"])

        assertEquals(listOf("b"), s.pairAllCandidates())
        assertEquals("a waiting row was counted as left alone", 0, s.pairing.leftAloneByPairAll(clock.get()))

        s.pairAllInherited()
        val offer = daemon.next()
        assertEquals("b", offer.payload?.optString("fingerprint"))
        offer.reply.complete(offered())
        awaitThat("the run did not end") { !s.pairing.running }
        assertEquals(0, daemon.offersTo("a"))
    }

    
    @Test
    fun aRowThatTurnsWaitingStaysAndOneThatTurnsInvitedLeaves() {
        val p = InheritedPairing(
            contacts = listOf(InheritedContactEntry("a", offers = 2), InheritedContactEntry("b", offers = 2)),
            linked = true,
        ).enqueue(listOf("a", "b")).first
        assertEquals(listOf("a", "a", "b", "b"), p.queue)

        val after = p.withContacts(
            listOf(
                InheritedContactEntry("a", devices = 2, paired = 1, offers = 1, waiting = true),
                InheritedContactEntry("b", devices = 2, paired = 1, offers = 1, invited = true),
            ),
            linked = true,
        )
        assertEquals(listOf("a", "a"), after.queue)
        assertEquals(PairState.WAITING, after.states["a"])
        assertNull("the invited row kept its run state", after.states["b"])
    }

    
    @Test
    fun pairAnywayQueuesEveryMissingDeviceWithForce() {
        val daemon = FakeDaemon()
        val s = store(daemon)
        s.dispatch(changedRows(row("a", devices = 2, offers = 2, waiting = true)))

        s.pairInherited("a", anyway = true)
        val first = daemon.next()
        assertEquals("a", first.payload?.optString("fingerprint"))
        assertTrue("Pair anyway went without force", first.payload!!.optBoolean("force"))
        assertEquals(listOf("a"), s.pairing.queue)
        first.reply.complete(offered())
        val second = daemon.next()
        assertTrue("Pair anyway went without force", second.payload!!.optBoolean("force"))
        second.reply.complete(offered())
        awaitThat("the run did not end") { !s.pairing.running }
        assertEquals(2, daemon.offersTo("a"))
    }

    
    @Test
    fun anInvitedRowIsNeitherQueuedNorAPairAllCandidate() {
        val p = InheritedPairing(
            contacts = listOf(
                InheritedContactEntry("a", devices = 2, offers = 2, invited = true),
                InheritedContactEntry("b", devices = 2, offers = 2, waiting = true, invited = true),
                InheritedContactEntry("c", offers = 1),
            ),
            linked = true,
        )
        for (anyway in listOf(false, true)) {
            val (next, joined) = p.enqueue(listOf("a", "b"), anyway)
            assertEquals("anyway=$anyway", 0, joined)
            assertTrue(next.queue.isEmpty())
        }
        assertEquals(listOf("c"), p.pairAllCandidates(NOW))
        assertEquals(0, p.leftAloneByPairAll(NOW))
    }

    
    @Test
    fun forceIsSentOnlyWhenTrue() {
        assertFalse(InvitationOfferRequest("a").toJson().has("force"))
        assertTrue(InvitationOfferRequest("a", force = true).toJson().getBoolean("force"))
    }

    
    @Test
    fun stopKeepsTheStateOfAnOfferAlreadyMade() {
        for (st in listOf(PairState.OFFERING, PairState.OFFERED)) {
            val p = InheritedPairing(
                contacts = listOf(InheritedContactEntry("a", offers = 2), InheritedContactEntry("b", offers = 2)),
                linked = true,
                states = mapOf("a" to st, "b" to PairState.WAITING),
                queue = listOf("a", "b", "b"),
            )
            val (stopped, n) = p.stopped()
            assertEquals("contacts, not queue entries", 2, n)
            assertTrue(stopped.queue.isEmpty())
            assertEquals("stop took the state of an offer already made", st, stopped.states["a"])
            assertNull(stopped.states["b"])

            val unlinked = p.withContacts(p.contacts, linked = false)
            assertTrue(unlinked.queue.isEmpty())
            assertEquals("an unlink took the state of an offer already made", st, unlinked.states["a"])
            assertNull(unlinked.states["b"])
            assertEquals(pairingStopped(2), unlinked.note)
        }
    }

    
    @Test
    fun theRowSaysHowManyDevicesOrWhoOffers() {
        assertEquals("1 of 2 devices", inheritedRowInfo(InheritedContactEntry("a", label = "Alice", devices = 2, paired = 1, offers = 1)))
        assertEquals(
            "waiting for Alice's device to offer",
            inheritedRowInfo(InheritedContactEntry("a", label = "Alice", devices = 2, paired = 1, waiting = true)),
        )
        assertEquals("the old row has no line", "", inheritedRowInfo(InheritedContactEntry("a", label = "Alice")))
        assertEquals(
            "accept Alice's invitation",
            inheritedRowInfo(InheritedContactEntry("a", label = "Alice", devices = 2, paired = 1, waiting = true, invited = true)),
        )
        assertEquals(
            "waiting for abcdef01…'s device to offer",
            inheritedRowInfo(InheritedContactEntry("abcdef0123456789", waiting = true)),
        )
    }

    @Test
    fun theContactOffersRefusalSaysWhoOffers() {
        val o = offerOutcome(refused("contact_offers"))
        assertEquals(PairState.FAILED, o.state)
        assertEquals(
            listOf(
                "That person's device offers this pairing, not this one.",
                "Nothing went out. Their invitation shows under invitations when it arrives; accept it there.",
            ),
            o.lines,
        )
        assertEquals(
            "This device is already paired with every device of that person it knows of.",
            explainOfferRefusal("already_paired").headline,
        )
        val invited = offerOutcome(refused("invitation_from_contact"))
        assertEquals(PairState.FAILED, invited.state)
        assertEquals(
            listOf(
                "That person's device has already sent this device an invitation.",
                "Nothing went out. Accept their invitation under invitations.",
            ),
            invited.lines,
        )
    }

    @Test
    fun theRowAsksBeforeRetiringAnInvitation() {
        assertEquals(RETIRE_CONFIRM_STANDING, retireConfirmFor(PairState.OFFERED))
        assertEquals(RETIRE_CONFIRM_MAY_STAND, retireConfirmFor(PairState.UNKNOWN))
        for (st in listOf("", PairState.FAILED, PairState.WAITING, PairState.OFFERING)) {
            assertNull("the row asks for $st", retireConfirmFor(st))
        }
    }

    
    @Test
    fun theSectionShowsWhenTheFirstReadFailed() {
        assertFalse(InheritedPairing().showSection)
        assertTrue(InheritedPairing(linked = true).showSection)
        assertTrue(InheritedPairing(listFailure = listOf("x")).showSection)
    }

    @Test
    fun readAgainWithNoDaemonSaysSo() {
        val daemon = FakeDaemon()
        val s = store(daemon, connected = false)
        s.requestInheritedContacts()
        s.requestInvitations()
        assertEquals(READ_AGAIN_NO_DAEMON, s.pairing.note)
        assertEquals(READ_AGAIN_NO_DAEMON, s.invitations.value.note)
        Thread.sleep(100)
        assertEquals(0, daemon.sent.size)
    }

    
    @Test
    fun theBootstrapReadsBothListsBeforeTheRunResumes() {
        val daemon = FakeDaemon()
        daemon.answers[FrameType.ListInheritedContacts] = listed(true, "b")
        daemon.answers[FrameType.InvitationsList] = invitationsListed()
        val s = holding(daemon, "a", "b", connected = false)
        s.pairInherited("a")
        s.pairInherited("b")
        awaitThat("the run did not hold") { !s.pairing.running }

        s._connection.value = true
        runBlocking { s.bootstrapPairingSurfaces() }
        val offer = daemon.next()
        assertEquals("b", offer.payload?.optString("fingerprint"))
        assertEquals(
            listOf(FrameType.ListInheritedContacts, FrameType.InvitationsList, FrameType.InvitationOffer),
            daemon.typesSent().take(3),
        )
        assertEquals("a contact who paired while away was offered", 0, daemon.offersTo("a"))
        offer.reply.complete(offered())
    }

    @Test
    fun theDaemonsStoppingDropsTheQueue() {
        val daemon = FakeDaemon()
        val s = holding(daemon, "a", connected = false)
        s.pairInherited("a")
        awaitThat("the run did not hold") { !s.pairing.running }

        s.onDaemonsStopped()
        assertTrue("the queue outlived the daemons", s.pairing.queue.isEmpty())
        assertEquals("", s.stateOf("a"))
        assertEquals(pairingStopped(1), s.pairing.note)
    }

    
    @Test
    fun anOfferAndAnAcceptAreGivenTheirLongTimeout() {
        val daemon = FakeDaemon()
        val s = holding(daemon, "a")
        s.pairInherited("a")
        daemon.next().reply.complete(offered())
        assertEquals(330.seconds, daemon.timeoutOf(FrameType.InvitationOffer))

        val t = offeredTo(daemon)
        t.acceptInvitation("m1")
        daemon.next().reply.complete(refused("rendezvous_unreachable"))
        assertEquals(330.seconds, daemon.timeoutOf(FrameType.InvitationAccept))
    }

    
    @Test
    fun everyRefusalTokenHasASentence() {
        for (t in OFFER_REASONS + CLIENT_MINTED) {
            val r = explainOfferRefusal(t)
            assertTrue("the offer refusal $t has no sentence", r.worded)
            assertFalse("the offer refusal $t shows its token", r.lines.any { t in it })
        }
        for (t in ACCEPT_REASONS + CLIENT_MINTED) {
            assertTrue("the accept refusal $t has no sentence", explainAcceptRefusal(t).worded)
        }
        for (t in INVITATIONS_LIST_REASONS + CLIENT_MINTED) {
            assertTrue("the invitations list refusal $t has no sentence", explainInvitationsListRefusal(t).worded)
        }
        for (t in INHERITED_LIST_REASONS + CLIENT_MINTED) {
            assertTrue("the inherited list refusal $t has no sentence", explainInheritedListRefusal(t).worded)
        }
    }

    
    @Test
    fun everyOfferTokenLeavesTheRowInTheRightState() {
        for (t in OFFER_REASONS) {
            val want = if (t == "encode_frame" || t == "nothing_open") PairState.OFFERED else PairState.FAILED
            assertEquals("the token $t", want, offerOutcome(refused(t)).state)
        }
        assertEquals(PairState.UNKNOWN, offerOutcome(refused(REASON_NO_ANSWER)).state)
    }

    
    @Test
    fun aFailedSelfCheckSaysTorIsSlowInOrange() {
        val o = offerOutcome(refused("self_check_failed"))
        assertEquals(PairState.FAILED, o.state)
        assertEquals(listOf(TOR_SLOW, "Nothing new went out."), o.lines)
        assertEquals(HaomaPalette.C_ATTENTION, detailColour(o.lines[0]))
        assertEquals(HaomaPalette.FG_DIM, detailColour(o.lines[1]))
    }

    
    @Test
    fun anUnknownTokenShowsItselfAndNotTheMessage() {
        val o = offerOutcome(refused("brand_new_token"))
        assertEquals(PairState.FAILED, o.state)
        assertTrue(o.lines.any { "brand_new_token" in it })
        assertFalse("the daemon's message reached the screen", o.lines.any { ".onion" in it })
    }

    
    @Test
    fun aCompletedPairingReadsBothListsAgain() {
        val daemon = FakeDaemon()
        daemon.answers[FrameType.ListInheritedContacts] = listed(true)
        daemon.answers[FrameType.InvitationsList] = invitationsListed()
        val s = store(daemon)
        s.dispatch(
            Frame(
                type = FrameType.PeerPaired,
                payload = JSONObject().put("peer_id", "p1").put("source", "invitation-offer"),
            ),
        )
        awaitThat("a completed pairing did not read the inherited contacts again") {
            daemon.countOf(FrameType.ListInheritedContacts) == 1
        }
        awaitThat("a completed pairing did not read the invitations again") {
            daemon.countOf(FrameType.InvitationsList) == 1
        }
    }

    @Test
    fun anInvitationMarkerReadsTheListAgain() {
        val daemon = FakeDaemon()
        daemon.answers[FrameType.InvitationsList] = invitationsListed(
            invitation("m1", InvitationDirection.IN, NOW + 3600, label = "Mara"),
        )
        val s = store(daemon)
        s.dispatch(
            Frame(
                type = FrameType.TimelineEvent,
                payload = JSONObject().put(
                    "event",
                    JSONObject()
                        .put("chat_id", "c1")
                        .put("kind", EventKind.INVITATION)
                        .put("direction", "in")
                        .put("msg_id", "m1")
                        .put("body", JSONObject().put("expires_at", NOW + 3600).put("state", "live")),
                ),
            ),
        )
        awaitThat("the invitation did not reach the list") { s.invitations.value.acceptable(NOW).size == 1 }
        assertEquals(1, daemon.countOf(FrameType.InvitationsList))
    }

    
    private fun offeredTo(daemon: FakeDaemon): MessengerStore {
        daemon.answers[FrameType.InvitationsList] = invitationsListed(
            invitation("m1", InvitationDirection.IN, NOW + 3600, label = "Mara"),
            invitation("m2", InvitationDirection.IN, NOW + 3600, label = "Toma"),
            invitation("m3", InvitationDirection.IN, NOW - 1, label = "Old"),
            invitation("m4", InvitationDirection.OUT, NOW + 3600, fingerprint = "x", label = "Mine"),
        )
        val s = store(daemon)
        runBlocking { s.refreshInvitations() }
        return s
    }

    
    @Test
    fun onlyAStandingInvitationOfferedHereIsAcceptable() {
        val s = offeredTo(FakeDaemon())
        assertEquals(listOf("m1", "m2"), s.invitations.value.acceptable(NOW).map { it.msgId })
    }

    @Test
    fun anAcceptThatSucceedsSaysSoAndReadsTheListAgain() {
        val daemon = FakeDaemon()
        val s = offeredTo(daemon)
        val listsBefore = daemon.countOf(FrameType.InvitationsList)

        s.acceptInvitation("m1")
        val accept = daemon.next()
        assertEquals("m1", accept.payload?.optString("msg_id"))
        assertEquals("m1", s.invitations.value.acceptingMsgId)

        
        s.acceptInvitation("m2")
        daemon.nothingMoreWaiting("a second accept went out while the first was running")

        accept.reply.complete(
            Frame(
                type = FrameType.InvitationAccepted,
                payload = JSONObject().put("msg_id", "m1").put("peer_id", "p").put("chat_id", "c"),
            ),
        )
        awaitThat("the accept did not end") { s.invitations.value.outcome != null }
        val outcome = s.invitations.value.outcome!!
        assertTrue(outcome.ok)
        assertEquals("invitation accepted — you can reach Mara from this device now", outcome.lines.first())
        assertEquals("", s.invitations.value.acceptingMsgId)
        awaitThat("the list was not read again after the accept") {
            daemon.countOf(FrameType.InvitationsList) == listsBefore + 1
        }
    }

    @Test
    fun anAcceptThatIsRefusedSaysWhyInWords() {
        val daemon = FakeDaemon()
        val s = offeredTo(daemon)
        s.acceptInvitation("m1")
        daemon.next().reply.complete(refused("rendezvous_unreachable"))
        awaitThat("the accept did not end") { s.invitations.value.outcome != null }
        val outcome = s.invitations.value.outcome!!
        assertFalse(outcome.ok)
        assertEquals(TOR_SLOW, outcome.lines.first())
        assertTrue("a dial with no answer is not drawn as a failure", outcome.torSlow)
        assertFalse("the daemon's message reached the screen", outcome.lines.any { ".onion" in it })
    }

    @Test
    fun anAcceptWhoseAnswerIsLostIsNoAnswer() {
        val daemon = FakeDaemon()
        val s = offeredTo(daemon)
        s.acceptInvitation("m1")
        assertNotNull(daemon.next())
        s._connection.value = false
        awaitThat("the lost answer was not recorded") { s.invitations.value.outcome != null }
        assertEquals("The accept had no answer.", s.invitations.value.outcome!!.lines.first())
    }

    @Test
    fun nothingIsSentForAnInvitationThatCannotBeAccepted() {
        val daemon = FakeDaemon()
        val s = offeredTo(daemon)
        s.acceptInvitation("m3")
        assertEquals(ACCEPT_NOT_STANDING, s.invitations.value.outcome!!.lines.first())
        s.acceptInvitation("m4")
        assertEquals(ACCEPT_OWN_INVITATION, s.invitations.value.outcome!!.lines.first())
        s._connection.value = false
        s.acceptInvitation("m1")
        assertEquals(ACCEPT_NOT_SENT, s.invitations.value.outcome!!.lines.first())
        assertEquals(0, daemon.countOf(FrameType.InvitationAccept))
    }

    companion object {
        const val NOW = 1_755_000_000L

        val CLIENT_MINTED = listOf("no_answer")

        val OFFER_REASONS = listOf(
            "not_ready", "bad_request", "backend_unavailable", "build_failed", "encode_frame",
            "unknown_contact", "already_paired", "no_sibling", "tag_failed",
            "not_inherited", "invite_failed", "rail_send_failed", "persist_failed",
            "key_stash_failed", "offer_in_progress", "self_check_failed",
            "contact_offers", "invitation_from_contact", "nothing_open",
        )

        val ACCEPT_REASONS = listOf(
            "not_ready", "bad_request", "backend_unavailable", "build_failed",
            "bad_payload", "encode_frame",
            "unknown_invitation", "invitation_not_live", "invitation_lapsed",
            "rendezvous_refused", "rendezvous_unreachable", "fingerprint_mismatch",
            "import_failed", "store_read_failed",
        )

        val INVITATIONS_LIST_REASONS = listOf("store_read_failed", "encode_frame")

        val INHERITED_LIST_REASONS = listOf("store_read_failed", "encode_frame")
    }
}
