package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.Frame
import io.haoma.calculator.core.ipc.FrameType
import io.haoma.calculator.messenger.settings.ACTION_VERIFY_DESTROY_PIN
import io.haoma.calculator.messenger.settings.LinkedDevicesView
import io.haoma.calculator.messenger.settings.linkedDevicesView
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import kotlin.time.Duration
import kotlin.time.Duration.Companion.seconds


@OptIn(ExperimentalCoroutinesApi::class)
class DeviceLinkRunTest {

    
    private class FakeDaemon {
        val sent = mutableListOf<Pair<String, JSONObject?>>()
        private val script = mutableMapOf<String, MutableList<Frame>>()

        fun answer(type: String, vararg frames: Frame): FakeDaemon {
            script.getOrPut(type) { mutableListOf() }.addAll(frames)
            return this
        }

        fun countOf(type: String): Int = sent.count { it.first == type }

        suspend fun handle(type: String, payload: JSONObject?): Frame {
            sent.add(type to payload)
            val queue = script[type] ?: return Frame(type = FrameType.Error)
            return if (queue.size == 1) queue[0] else queue.removeAt(0)
        }
    }

    private fun store(
        daemon: FakeDaemon,
        nowSeconds: Long = 1_000L,
    ) = MessengerStore(
        requestSender = { type, payload -> daemon.handle(type, payload) },
        deviceLinkTimings = DeviceLinkTimings(
            saltWaitMs = 100L,
            saltPollMs = 10L,
            nowSeconds = { nowSeconds },
        ),
    ).also {
        
        
        it._connection.value = true
    }

    
    private fun started(handle: String, expiresAt: Long) = Frame(
        type = FrameType.DeviceLinkStarted,
        payload = JSONObject()
            .put("handle_id", handle)
            .put("words", JSONArray(listOf("a", "b", "c", "d", "e", "f", "g")))
            .put("expires_at", expiresAt),
    )

    private fun accepted(peerId: String) = Frame(
        type = FrameType.DeviceLinkAccepted,
        payload = JSONObject().put("peer_id", peerId).put("adopted_fingerprint", "FEED FACE"),
    )

    private fun pinSet(accepted: Boolean, reason: String = "") = Frame(
        type = FrameType.DestroyPinSet,
        payload = JSONObject().put("accepted", accepted).put("reason", reason),
    )

    private fun guardListed(set: Boolean, siblingWithSalt: String? = null) = Frame(
        type = FrameType.DestroyPinListed,
        payload = JSONObject()
            .put("set", set)
            .put(
                "siblings",
                JSONArray().apply {
                    if (siblingWithSalt != null) {
                        put(JSONObject().put("peer_id", siblingWithSalt).put("salt_held", true))
                    }
                },
            ),
    )

    private fun verified(answered: Boolean, verified: Boolean, reason: String = "") = Frame(
        type = FrameType.DestroyPinVerified,
        payload = JSONObject().put("answered", answered).put("verified", verified).put("reason", reason),
    )

    private fun devicesListed(vararg peerIds: String) = Frame(
        type = FrameType.DeviceLinksListed,
        payload = JSONObject().put(
            "devices",
            JSONArray().apply { peerIds.forEach { put(JSONObject().put("peer_id", it)) } },
        ),
    )

    private fun failedPush(handle: String, reason: String, detail: String = "") =
        DeviceLinkFailedPush(handleId = handle, reason = reason, detail = detail)

    
    private val MessengerStore.run: DeviceLinkRun get() = _deviceLinkRun.value!!
    private val DeviceLinkRun.lastLine: String get() = log.last().text
    private fun DeviceLinkRun.saysAnywhere(text: String) = log.any { it.text == text }

    
    private suspend fun MessengerStore.offeringRun() {
        beginDeviceLinkRun(DeviceLinkMode.Inviting)
        setDeviceLinkPins("1111", "2222")
        proceedDeviceLink()
        assertEquals(DeviceLinkPhase.Working, run.phase)
    }

    
    @Test
    fun twoEqualPinsAreRefusedAndTheRunDoesNotStart() = runTest {
        val daemon = FakeDaemon()
        val s = store(daemon)
        s.beginDeviceLinkRun(DeviceLinkMode.Inviting)
        s.setDeviceLinkPins("1234", "1234")
        s.proceedDeviceLink()

        assertEquals(PIN_MUST_DIFFER, s.run.lastLine)
        assertEquals(DeviceLinkPhase.Collecting, s.run.phase)
        assertEquals("a refused run sent something", 0, daemon.sent.size)
    }

    @Test
    fun aPinThatIsNotFourDigitsIsRefused() = runTest {
        val daemon = FakeDaemon()
        val s = store(daemon)
        s.beginDeviceLinkRun(DeviceLinkMode.Inviting)
        s.setDeviceLinkPins("123", "4567")
        s.proceedDeviceLink()

        assertEquals(PIN_FOUR_DIGITS, s.run.lastLine)
        assertEquals(DeviceLinkPhase.Collecting, s.run.phase)
        assertEquals(0, daemon.sent.size)
    }

    @Test
    fun aPastedPinIsTrimmedBeforeItIsChecked() = runTest {
        val daemon = FakeDaemon().answer(FrameType.DeviceLinkOffer, started("h-1", 5_000L))
        val s = store(daemon)
        s.beginDeviceLinkRun(DeviceLinkMode.Inviting)
        
        s.setDeviceLinkPins(" 1234 ", "\t5678\n")
        s.proceedDeviceLink()

        assertFalse("a pasted PIN was refused for its whitespace", s.run.saysAnywhere(PIN_FOUR_DIGITS))
        assertEquals(DeviceLinkPhase.Working, s.run.phase)
        assertEquals("1234", s.run.myPin)
        assertEquals("5678", s.run.otherPin)
    }

    @Test
    fun theNoteSpeaksOnlyAboutACompletePair() = runTest {
        val s = store(FakeDaemon())
        s.beginDeviceLinkRun(DeviceLinkMode.Inviting)

        s.setDeviceLinkPins("1", "1")
        assertEquals("the note spoke while a field was short", "", s.run.note)
        s.setDeviceLinkPins("1234", "1")
        assertEquals("", s.run.note)
        s.setDeviceLinkPins("1234", "12")
        assertEquals("", s.run.note)
        s.setDeviceLinkPins("1234", "1234")
        assertEquals(PIN_MUST_DIFFER, s.run.note)
        s.setDeviceLinkPins("1234", "5678")
        assertEquals("the note refused a pair that is fine", "", s.run.note)
    }

    @Test
    fun aJoiningRunWithTwoWordsIsRefused() = runTest {
        val daemon = FakeDaemon()
        val s = store(daemon)
        s.beginDeviceLinkRun(DeviceLinkMode.Joining)
        s.setDeviceLinkPins("1234", "5678")
        s.setDeviceLinkWords("alpha bravo")
        s.proceedDeviceLink()

        assertEquals(WORDS_NEEDED, s.run.lastLine)
        assertEquals(DeviceLinkPhase.Collecting, s.run.phase)
        assertEquals(0, daemon.sent.size)
    }

    
    @Test
    fun aCompletionForAClosedRunChangesNothing() = runTest {
        val daemon = FakeDaemon().answer(FrameType.DeviceLinkOffer, started("h-1", 5_000L))
        val s = store(daemon)
        s.offeringRun()
        val sentByTheOffer = daemon.sent.size

        daemon.answer(FrameType.DeviceLinksList, devicesListed("peer-1"))
        s.endDeviceLinkRun()
        s.takeDeviceLinkCompletion(DeviceLinkCompletedPush(handleId = "h-1", peerId = "peer-1"))

        assertNull("a closed run came back", s._deviceLinkRun.value)
        
        
        assertEquals(
            "a closed run sent a frame other than the linked-device read",
            listOf(FrameType.DeviceLinksList),
            daemon.sent.drop(sentByTheOffer).map { it.first },
        )
        assertEquals(listOf("peer-1"), s.linkedDevices.value.map { it.peerId })
    }

    @Test
    fun aStepThatFinishesLateDoesNotWriteOntoTheNextRun() = runTest {
        
        
        lateinit var s: MessengerStore
        var secondRun = 0L
        var reads = 0
        s = MessengerStore(
            deviceLinkTimings = DeviceLinkTimings(saltWaitMs = 100L, saltPollMs = 10L),
            requestSender = { type, _ ->
                when (type) {
                    FrameType.SetDestroyPin -> pinSet(true)
                    FrameType.GetDestroyPin -> {
                        reads++
                        if (reads == 2) {
                            
                            s.endDeviceLinkRun()
                            s.beginDeviceLinkRun(DeviceLinkMode.Inviting)
                            s.setDeviceLinkPins("3333", "4444")
                            secondRun = s.run.id
                        }
                        guardListed(set = false)
                    }
                    else -> Frame(type = FrameType.Error)
                }
            },
        )
        s.beginDeviceLinkRun(DeviceLinkMode.Inviting)
        s.setDeviceLinkPins("1111", "2222")
        s._deviceLinkRun.value = s.run.copy(phase = DeviceLinkPhase.Working, peerId = "peer-1")
        val firstRun = s.run.id

        s.runGuardSteps(firstRun, "peer-1")

        assertTrue("the second run never opened", secondRun != 0L)
        assertEquals("the closed run replaced the live one", secondRun, s.run.id)
        assertEquals("the closed run wrote onto the second run", emptyList<DeviceLinkLogLine>(), s.run.log)
        assertEquals(DeviceLinkPhase.Collecting, s.run.phase)
        assertFalse("the closed run failed the second one", s.run.failed)
        assertEquals("3333", s.run.myPin)
        
        
        assertEquals("the closed run kept asking the daemon", 2, reads)
    }

    
    @Test
    fun aCompletionArrivingTwiceRunsTheGuardStepsOnce() = runTest {
        
        
        val push = DeviceLinkCompletedPush(handleId = "h-1", peerId = "peer-1")
        lateinit var s: MessengerStore
        var writes = 0
        var checks = 0
        var secondDelivered = false
        s = MessengerStore(
            deviceLinkTimings = DeviceLinkTimings(saltWaitMs = 100L, saltPollMs = 10L),
            requestSender = { type, _ ->
                when (type) {
                    FrameType.DeviceLinkOffer -> started("h-1", 5_000L)
                    FrameType.SetDestroyPin -> { writes++; pinSet(true) }
                    FrameType.GetDestroyPin -> {
                        if (!secondDelivered) {
                            secondDelivered = true
                            
                            
                            s.takeDeviceLinkCompletion(push)
                        }
                        guardListed(set = false, siblingWithSalt = "peer-1")
                    }
                    FrameType.VerifyDestroyPin -> { checks++; verified(answered = true, verified = true) }
                    else -> Frame(type = FrameType.Error)
                }
            },
        )
        
        
        s._connection.value = true
        s.offeringRun()

        s.takeDeviceLinkCompletion(push)

        assertTrue("the second completion was never delivered", secondDelivered)
        assertEquals("the guard was written twice", 1, writes)
        assertEquals("the check ran twice", 1, checks)
        assertEquals(CEREMONY_DONE, s.run.lastLine)
        assertEquals(DeviceLinkPhase.Done, s.run.phase)
    }

    @Test
    fun aCompletionWhileTheRunIsStillCollectingIsDropped() = runTest {
        val daemon = FakeDaemon()
        val s = store(daemon)
        s.beginDeviceLinkRun(DeviceLinkMode.Inviting)
        s.setDeviceLinkPins("1111", "2222")
        
        
        s.takeDeviceLinkCompletion(DeviceLinkCompletedPush(handleId = "", peerId = "peer-1"))

        assertEquals(0, daemon.countOf(FrameType.SetDestroyPin))
        assertEquals(DeviceLinkPhase.Collecting, s.run.phase)
        assertEquals(emptyList<DeviceLinkLogLine>(), s.run.log)
    }

    
    @Test
    fun aFailurePushNamingAnotherHandleDoesNotEndARunThatHoldsOne() = runTest {
        val daemon = FakeDaemon().answer(FrameType.DeviceLinkOffer, started("h-1", 5_000L))
        val s = store(daemon)
        s.offeringRun()

        s.deviceLinkFailed(failedPush("h-other", "expired"))

        assertEquals("a stranger's failure ended this run", DeviceLinkPhase.Working, s.run.phase)
        assertFalse(s.run.failed)
    }

    @Test
    fun aFailurePushNamingAnyHandleEndsARunThatHoldsNone() = runTest {
        
        
        val s = store(FakeDaemon())
        s.beginDeviceLinkRun(DeviceLinkMode.Joining)
        s.setDeviceLinkPins("1111", "2222")
        s.setDeviceLinkWords("a b c d e f g")
        
        
        s._deviceLinkRun.value = s.run.copy(phase = DeviceLinkPhase.Working)
        assertEquals("", s.run.handleId)

        s.deviceLinkFailed(failedPush("h-made-elsewhere", "bad_words"))

        assertTrue(s.run.failed)
        assertEquals(DeviceLinkPhase.Done, s.run.phase)
        assertTrue(s.run.saysAnywhere("Those words did not open a link offer."))
    }

    @Test
    fun aRefusalLandsInTheRunsOwnLog() = runTest {
        val daemon = FakeDaemon().answer(FrameType.DeviceLinkOffer, started("h-1", 5_000L))
        val s = store(daemon)
        s.offeringRun()
        val statusLinesBefore = s.statusLog.value.size

        s.deviceLinkFailed(failedPush("h-1", "expired"))

        assertTrue(s.run.saysAnywhere("The offer ran out before the other device joined."))
        assertEquals(
            "a refusal went to a screen the person is not standing on",
            statusLinesBefore,
            s.statusLog.value.size,
        )
    }

    
    @Test
    fun everyRefusalTokenGetsItsOwnSentence() {
        val table = listOf(
            "no_answer" to "The link may have gone through.",
            "not_empty" to "This device already has contacts or chats, so it cannot join another device.",
            "has_contact" to "This device already has contacts or chats, so it cannot join another device.",
            "has_chat" to "This device already has contacts or chats, so it cannot join another device.",
            "has_peer" to "This device already has contacts or chats, so it cannot join another device.",
            "nonempty" to "This device already has contacts or chats, so it cannot join another device.",
            "peer_check" to "This device could not confirm it is a clean slate, so it did not join.",
            "registry" to "This device could not confirm it is a clean slate, so it did not join.",
            "bundle" to "The other device never sent its identity, so this device stayed unchanged.",
            "expire" to "The offer ran out before the other device joined.",
            "timeout" to "The offer ran out before the other device joined.",
            "timed_out" to "The offer ran out before the other device joined.",
            "cancel" to "The offer was withdrawn before the other device joined.",
            "withdraw" to "The offer was withdrawn before the other device joined.",
            "already_linked" to "This device already has a linked device, and you can only have one.",
            "already_a_device" to "This device already has a linked device, and you can only have one.",
            "word" to "Those words did not open a link offer.",
            "unreachable" to "This device could not reach the one that made the offer.",
            "dial" to "This device could not reach the one that made the offer.",
            "connect" to "This device could not reach the one that made the offer.",
        )
        for ((token, sentence) in table) {
            assertEquals(token, sentence, explainDeviceLinkFailure(token, "").headline)
        }
        
        assertEquals(
            "The offer ran out before the other device joined.",
            explainDeviceLinkFailure("  TIMED-OUT ", "").headline,
        )
        
        assertEquals(9, table.map { it.second }.toSet().size)
    }

    @Test
    fun anUnknownTokenFallsBackAndCarriesItself() {
        val out = explainDeviceLinkFailure("wobbly_thing", "the rail said no")
        assertEquals(FAILURE_FALLBACK, out.headline)
        assertEquals(listOf("wobbly_thing: the rail said no"), out.guidance)

        val bare = explainDeviceLinkFailure("wobbly_thing", "")
        assertEquals(listOf("wobbly_thing"), bare.guidance)
    }

    @Test
    fun anEmptyTokenShowsTheDetailIfThereIsOne() {
        assertEquals(FAILURE_FALLBACK, explainDeviceLinkFailure("", "").headline)
        assertEquals(emptyList<String>(), explainDeviceLinkFailure("", "").guidance)
        assertEquals(listOf("the rail said no"), explainDeviceLinkFailure("", "the rail said no").guidance)
    }

    
    @Test
    fun aRefusedGuardWriteLeavesTheRunRetryableAndSaysTheLinkStands() = runTest {
        val daemon = FakeDaemon()
            .answer(FrameType.DeviceLinkOffer, started("h-1", 5_000L))
            .answer(FrameType.SetDestroyPin, pinSet(accepted = false, reason = DestroyPinSetReason.ALREADY_SET))
        val s = store(daemon)
        s.offeringRun()
        s.takeDeviceLinkCompletion(DeviceLinkCompletedPush(handleId = "h-1", peerId = "peer-1"))

        assertTrue("a refused guard write left no way back", s.run.retryable)
        assertTrue(s.run.failed)
        assertEquals(GUARD_NOT_TAKEN, s.run.lastLine)
        assertTrue("the sentence does not say the link stands", GUARD_NOT_TAKEN.contains("linked"))
        
        assertEquals("1111", s.run.myPin)
        assertEquals("2222", s.run.otherPin)
        assertEquals("the check ran on a guard that was never written", 0, daemon.countOf(FrameType.VerifyDestroyPin))
    }

    
    @Test
    fun aSaltWaitThatTimesOutEndsTheRunLinkedNotFailed() = runTest {
        val daemon = FakeDaemon()
            .answer(FrameType.DeviceLinkOffer, started("h-1", 5_000L))
            .answer(FrameType.SetDestroyPin, pinSet(true))
            
            .answer(FrameType.GetDestroyPin, guardListed(set = false))
        val s = store(daemon)
        s.offeringRun()
        s.takeDeviceLinkCompletion(DeviceLinkCompletedPush(handleId = "h-1", peerId = "peer-1"))

        assertEquals("a run left working draws no way out", DeviceLinkPhase.Done, s.run.phase)
        assertFalse("a run whose link came up was marked as ended badly", s.run.failed)
        assertFalse("the spinner is still turning", s.run.busy)
        assertEquals(GUARD_NO_DETAILS, s.run.lastLine)
        assertEquals(DeviceLinkOutcome.Done, s.run.log.last().outcome)
        assertTrue("the sentence does not say the two are linked", GUARD_NO_DETAILS.startsWith("linked"))
        assertTrue("the sentence does not say what is missing", GUARD_NO_DETAILS.contains("details not received yet"))
        assertFalse(
            "a step is marked failed on a run that linked",
            s.run.log.any { it.outcome == DeviceLinkOutcome.Failed },
        )
        assertEquals("the check ran without the salt", 0, daemon.countOf(FrameType.VerifyDestroyPin))
        
        assertEquals("no sync was asked for after the salt wait", 1, daemon.countOf(FrameType.DeviceSync))
    }

    @Test
    fun theSaltWaitEndsAsSoonAsTheDetailsLand() = runTest {
        val daemon = FakeDaemon()
            .answer(FrameType.DeviceLinkOffer, started("h-1", 5_000L))
            .answer(FrameType.SetDestroyPin, pinSet(true))
            
            .answer(
                FrameType.GetDestroyPin,
                guardListed(set = false),
                guardListed(set = false),
                guardListed(set = false, siblingWithSalt = "peer-1"),
            )
            .answer(FrameType.VerifyDestroyPin, verified(answered = true, verified = true))
        val s = store(daemon)
        s.offeringRun()
        s.takeDeviceLinkCompletion(DeviceLinkCompletedPush(handleId = "h-1", peerId = "peer-1"))

        assertEquals(CEREMONY_DONE, s.run.lastLine)
        assertEquals(DeviceLinkPhase.Done, s.run.phase)
        assertFalse(s.run.failed)
        assertEquals(3, daemon.countOf(FrameType.GetDestroyPin))
    }

    
    private suspend fun endingFor(answer: Frame): DeviceLinkRun {
        val daemon = FakeDaemon()
            .answer(FrameType.DeviceLinkOffer, started("h-1", 5_000L))
            .answer(FrameType.SetDestroyPin, pinSet(true))
            .answer(FrameType.GetDestroyPin, guardListed(set = false, siblingWithSalt = "peer-1"))
            .answer(FrameType.VerifyDestroyPin, answer)
        val s = store(daemon)
        s.offeringRun()
        s.takeDeviceLinkCompletion(DeviceLinkCompletedPush(handleId = "h-1", peerId = "peer-1"))
        return s.run
    }

    @Test
    fun aVerifyNobodyAnsweredAndAVerifyAnsweredNoAreDifferentSentences() = runTest {
        val off = endingFor(verified(answered = false, verified = false))
        val refused = endingFor(verified(answered = true, verified = false))
        val good = endingFor(verified(answered = true, verified = true))

        assertEquals(LINKED_VERIFY_LATER, off.lastLine)
        assertTrue("the run does not say the other device did not answer", off.saysAnywhere(VERIFY_NO_ANSWER))
        assertEquals(VERIFY_REFUSED, refused.lastLine)
        assertEquals(CEREMONY_DONE, good.lastLine)
        assertTrue(off.lastLine != refused.lastLine)
        assertFalse(off.failed)
        assertTrue(refused.failed)
        assertFalse(good.failed)
        
        assertFalse(refused.log.any { it.text.contains("cool") || it.text.contains("wrong") })
    }

    
    @Test
    fun aCheckNobodyAnsweredEndsTheRunLinkedWithAWarning() = runTest {
        val run = endingFor(verified(answered = false, verified = false))

        assertEquals("a run left working draws no way out", DeviceLinkPhase.Done, run.phase)
        assertFalse("a run whose link came up was marked as ended badly", run.failed)
        assertFalse("a run that linked was offered the guard retry", run.retryable)
        assertFalse("the spinner is still turning", run.busy)
        assertFalse(
            "a step is marked failed on a run that linked",
            run.log.any { it.outcome == DeviceLinkOutcome.Failed },
        )

        val check = run.log.last { it.text == GUARD_CHECKING }
        assertEquals("the check step does not carry the warning", DeviceLinkOutcome.Warning, check.outcome)
        val reason = run.log[run.log.indexOf(check) + 1]
        assertEquals(VERIFY_NO_ANSWER, reason.text)
        assertTrue("the reason is drawn as a step of its own", reason.aside)

        assertEquals(LINKED_VERIFY_LATER, run.lastLine)
        assertEquals(
            "Linked. Please press [Verify destroy PIN] later to check your other device's PIN.",
            LINKED_VERIFY_LATER,
        )
        assertTrue(
            "the last line does not name the linked view's button",
            LINKED_VERIFY_LATER.contains("[$ACTION_VERIFY_DESTROY_PIN]"),
        )
        assertEquals("the run does not say the setting step finished", DeviceLinkOutcome.Done, run.log.first { it.text == GUARD_SETTING }.outcome)
    }

    
    @Test
    fun aCheckAnsweredNoStillFailsTheRun() = runTest {
        val run = endingFor(verified(answered = true, verified = false))

        assertTrue(run.failed)
        assertEquals(DeviceLinkPhase.Done, run.phase)
        assertEquals(VERIFY_REFUSED, run.lastLine)
        assertEquals(DeviceLinkOutcome.Failed, run.log.last { it.text == GUARD_CHECKING }.outcome)
        assertFalse(run.saysAnywhere(LINKED_VERIFY_LATER))
        assertFalse(run.log.any { it.outcome == DeviceLinkOutcome.Warning })
    }

    
    @Test
    fun aCheckHaomaDidNotSendStillFailsTheRun() = runTest {
        val run = endingFor(verified(answered = false, verified = false, reason = DestroyPinVerifiedReason.NO_SALT_FOR_SIBLING))

        assertTrue(run.failed)
        assertEquals(VERIFY_NO_DETAILS, run.lastLine)
        assertFalse(run.saysAnywhere(VERIFY_NO_ANSWER))
        assertFalse(run.saysAnywhere(LINKED_VERIFY_LATER))
        assertFalse(run.log.any { it.outcome == DeviceLinkOutcome.Warning })
    }

    
    @Test
    fun aReplyThatDidNotCheckOutFailsTheRun() = runTest {
        val run = endingFor(verified(answered = false, verified = false, reason = DestroyPinVerifiedReason.ANSWER_DID_NOT_CHECK_OUT))

        assertTrue(run.failed)
        assertEquals(VERIFY_ANSWER_BAD, run.lastLine)
        assertEquals(DeviceLinkOutcome.Failed, run.log.last { it.text == GUARD_CHECKING }.outcome)
        assertFalse(run.saysAnywhere(VERIFY_NO_ANSWER))
        assertFalse(run.saysAnywhere(LINKED_VERIFY_LATER))
    }

    
    @Test
    fun aCheckThatCouldNotBeSentEndsTheRunLinkedWithAWarning() = runTest {
        
        val run = endingFor(Frame(type = FrameType.Error, payload = JSONObject().put("code", "backend_unavailable")))

        assertFalse(run.failed)
        assertEquals(DeviceLinkPhase.Done, run.phase)
        assertEquals(DeviceLinkOutcome.Warning, run.log.last { it.text == GUARD_CHECKING }.outcome)
        assertTrue(run.saysAnywhere(VERIFY_NOT_SENT))
        assertEquals(LINKED_VERIFY_LATER, run.lastLine)
    }

    
    @Test
    fun aConnectionThatDropsDuringTheCheckEndsTheRunLinkedAtOnce() = runTest {
        val daemon = FakeDaemon()
            .answer(FrameType.DeviceLinkOffer, started("h-1", 5_000L))
            .answer(FrameType.SetDestroyPin, pinSet(true))
            .answer(FrameType.GetDestroyPin, guardListed(set = false, siblingWithSalt = "peer-1"))
        lateinit var s: MessengerStore
        s = MessengerStore(
            requestSender = { type, payload ->
                if (type == FrameType.VerifyDestroyPin) {
                    daemon.sent.add(type to payload)
                    s._connection.value = false
                    awaitCancellation()
                }
                daemon.handle(type, payload)
            },
            deviceLinkTimings = DeviceLinkTimings(saltWaitMs = 100L, saltPollMs = 10L, nowSeconds = { 1_000L }),
        ).also { it._connection.value = true }
        s.offeringRun()
        s.takeDeviceLinkCompletion(DeviceLinkCompletedPush(handleId = "h-1", peerId = "peer-1"))

        assertEquals(1, daemon.countOf(FrameType.VerifyDestroyPin))
        assertEquals(DeviceLinkPhase.Done, s.run.phase)
        assertFalse(s.run.failed)
        assertTrue(s.run.saysAnywhere(VERIFY_DAEMON_NO_ANSWER))
        assertEquals(LINKED_VERIFY_LATER, s.run.lastLine)
    }

    
    @Test
    fun theLongWaitsAreGivenTheirOwnLimits() = runTest {
        val waits = mutableListOf<Pair<String, Duration>>()
        val daemon = FakeDaemon()
            .answer(FrameType.DeviceLinkOffer, started("h-1", 5_000L))
            .answer(FrameType.SetDestroyPin, pinSet(true))
            .answer(FrameType.GetDestroyPin, guardListed(set = false, siblingWithSalt = "peer-1"))
            .answer(FrameType.VerifyDestroyPin, verified(answered = true, verified = true))
        val s = MessengerStore(
            timedRequestSender = { type, payload, timeout ->
                waits.add(type to timeout)
                daemon.handle(type, payload)
            },
            requestSender = { type, payload -> daemon.handle(type, payload) },
            deviceLinkTimings = DeviceLinkTimings(saltWaitMs = 100L, saltPollMs = 10L, nowSeconds = { 1_000L }),
        ).also { it._connection.value = true }
        s.offeringRun()
        s.takeDeviceLinkCompletion(DeviceLinkCompletedPush(handleId = "h-1", peerId = "peer-1"))
        assertEquals(CEREMONY_DONE, s.run.lastLine)

        val runCheck = waits.first { it.first == FrameType.VerifyDestroyPin }
        
        assertTrue("the run's check was given ${runCheck.second}", runCheck.second > 150.seconds)
        assertEquals(DEVICE_LINK_VERIFY_TIMEOUT, runCheck.second)

        s.verifyOtherDestroyPin("peer-1", "2222")
        assertEquals(DEVICE_LINK_VERIFY_TIMEOUT, waits.last { it.first == FrameType.VerifyDestroyPin }.second)

        
        s.destroyLinkedDevice("peer-1", "2222")
        val destroy = waits.single { it.first == FrameType.UnlinkDevice }
        assertEquals(DEVICE_LINK_DESTROY_TIMEOUT, destroy.second)
        assertTrue("the destroy was given ${destroy.second}", destroy.second > 210.seconds)
    }

    
    @Test
    fun aSecondActionWhileOneRunsIsRefusedAndSendsNothing() = runTest {
        val release = CompletableDeferred<Frame>()
        val daemon = FakeDaemon()
        val s = MessengerStore(
            requestSender = { type, payload ->
                if (type == FrameType.VerifyDestroyPin) {
                    daemon.sent.add(type to payload)
                    release.await()
                } else {
                    daemon.handle(type, payload)
                }
            },
        ).also { it._connection.value = true }

        val first = launch { s.verifyOtherDestroyPin("peer-1", "2222") }
        runCurrent()
        assertEquals(ACTION_LABEL_VERIFY, s.linkedAction.value.busyLabel)

        assertEquals(LINKED_ACTION_RUNNING, s.verifyOtherDestroyPin("peer-1", "2222").text)
        assertEquals(LINKED_ACTION_RUNNING, s.destroyLinkedDevice("peer-1", "2222").text)
        assertEquals(LINKED_ACTION_RUNNING, s.unlinkLinkedDevice("peer-1").text)
        assertEquals("a second check went out", 1, daemon.countOf(FrameType.VerifyDestroyPin))
        assertEquals("a command went out during a check", 0, daemon.countOf(FrameType.UnlinkDevice))
        assertEquals("the refusal took the running action's place", ACTION_LABEL_VERIFY, s.linkedAction.value.busyLabel)

        release.complete(verified(answered = true, verified = true))
        first.join()
        assertEquals("", s.linkedAction.value.busyLabel)
        assertEquals(VERIFY_ACCEPTED, s.linkedAction.value.note)
        assertEquals("peer-1", s.linkedAction.value.peerId)
    }

    
    @Test
    fun theActionLabelsAreTheButtonLabels() {
        assertEquals(ACTION_VERIFY_DESTROY_PIN, ACTION_LABEL_VERIFY)
        assertEquals(io.haoma.calculator.messenger.settings.ACTION_UNLINK, ACTION_LABEL_UNLINK)
        assertEquals(io.haoma.calculator.messenger.settings.ACTION_DESTROY, ACTION_LABEL_DESTROY)
    }

    
    @Test
    fun aConnectionThatDropsDuringAnActionSaysWhatIsKnown() = runTest {
        lateinit var s: MessengerStore
        s = MessengerStore(
            requestSender = { _, _ ->
                s._connection.value = false
                awaitCancellation()
            },
        )
        s._connection.value = true
        assertEquals(VERIFY_DAEMON_NO_ANSWER, s.verifyOtherDestroyPin("peer-1", "2222").text)
        s._connection.value = true
        assertEquals(DESTROY_DAEMON_NO_ANSWER, s.destroyLinkedDevice("peer-1", "2222").text)
        s._connection.value = true
        assertEquals(UNLINK_DAEMON_NO_ANSWER, s.unlinkLinkedDevice("peer-1").text)
        assertEquals("the action is still marked running", "", s.linkedAction.value.busyLabel)
    }

    
    @Test
    fun theWordsAreHeldBackUntilAProbeSaysReady() = runTest {
        val daemon = FakeDaemon().answer(FrameType.DeviceLinkOffer, started("h-1", 5_000L))
        val s = store(daemon)
        s.offeringRun()
        assertFalse("the words were released before any probe", s.run.wordsReleased)

        s.deviceLinkProbe(DeviceLinkProbePush("h-1", attempt = 2, ready = false, error = "", at = 0L))
        assertFalse(s.run.wordsReleased)
        assertEquals(probeStillWaiting(2), s.run.lastLine)

        s.deviceLinkProbe(DeviceLinkProbePush("h-1", attempt = 3, ready = true, error = "", at = 0L))
        assertTrue(s.run.wordsReleased)
        assertEquals(PROBE_REACHABLE, s.run.lastLine)
    }

    @Test
    fun aReadyProbeCarryingATokenDoesNotSayTheDeviceIsReachable() = runTest {
        val daemon = FakeDaemon().answer(FrameType.DeviceLinkOffer, started("h-1", 5_000L))
        val s = store(daemon)
        s.offeringRun()

        
        s.deviceLinkProbe(DeviceLinkProbePush("h-1", attempt = 4, ready = true, error = "probe-timeout", at = 0L))

        assertTrue(s.run.wordsReleased)
        assertEquals(PROBE_UNCONFIRMED, s.run.lastLine)
        assertFalse("a device nobody reached was called reachable", s.run.saysAnywhere(PROBE_REACHABLE))
    }

    
    @Test
    fun aReconnectWithALinkedDeviceCarriesOnWithTheGuardSteps() = runTest {
        val daemon = FakeDaemon()
            .answer(FrameType.DeviceLinkOffer, started("h-1", 5_000L))
            .answer(FrameType.DeviceLinksList, devicesListed("peer-1"))
            .answer(
                FrameType.GetDestroyPin,
                
                guardListed(set = false),
                guardListed(set = false, siblingWithSalt = "peer-1"),
            )
            .answer(FrameType.SetDestroyPin, pinSet(true))
            .answer(FrameType.VerifyDestroyPin, verified(answered = true, verified = true))
        val s = store(daemon)
        s.offeringRun()
        

        s.reconcileDeviceLinkAfterConnect()

        assertEquals("the run kept spinning", DeviceLinkPhase.Done, s.run.phase)
        assertFalse(s.run.failed)
        assertEquals(CEREMONY_DONE, s.run.lastLine)
        assertEquals("peer-1", s.run.peerId)
        assertEquals(1, daemon.countOf(FrameType.SetDestroyPin))
    }

    @Test
    fun aReconnectWithNoLinkedDeviceAndTheDeadlinePassedEndsTheRun() = runTest {
        val daemon = FakeDaemon()
            .answer(FrameType.DeviceLinkOffer, started("h-1", 900L))
            .answer(FrameType.DeviceLinksList, devicesListed())
            .answer(FrameType.GetDestroyPin, guardListed(set = false))
        
        val s = store(daemon, nowSeconds = 1_000L)
        s.offeringRun()

        s.reconcileDeviceLinkAfterConnect()

        assertEquals(DeviceLinkPhase.Done, s.run.phase)
        assertTrue(s.run.failed)
        assertFalse(s.run.busy)
        assertTrue(s.run.saysAnywhere(OFFER_RAN_OUT))
        assertTrue("nobody said whether anything was linked", s.run.saysAnywhere(NOTHING_LINKED))
    }

    @Test
    fun aReconnectWithNoLinkedDeviceAndTimeLeftKeepsWaiting() = runTest {
        val daemon = FakeDaemon()
            .answer(FrameType.DeviceLinkOffer, started("h-1", 5_000L))
            .answer(FrameType.DeviceLinksList, devicesListed())
            .answer(FrameType.GetDestroyPin, guardListed(set = false))
        
        val s = store(daemon, nowSeconds = 1_000L)
        s.offeringRun()

        s.reconcileDeviceLinkAfterConnect()

        
        assertEquals("nothing was known and the run was ended anyway", DeviceLinkPhase.Working, s.run.phase)
        assertFalse("nothing was known and a failure was reported", s.run.failed)
        assertFalse(s.run.saysAnywhere(OFFER_RAN_OUT))
        assertEquals(
            "the drop was hidden from the person",
            RECONNECTED,
            s.run.lastLine,
        )
    }

    @Test
    fun aReconnectDuringACollectingRunLeavesItAlone() = runTest {
        val daemon = FakeDaemon()
            .answer(FrameType.DeviceLinksList, devicesListed("peer-1"))
            .answer(FrameType.GetDestroyPin, guardListed(set = true))
        val s = store(daemon)
        s.beginDeviceLinkRun(DeviceLinkMode.Inviting)
        s.setDeviceLinkPins("1111", "2222")

        s.reconcileDeviceLinkAfterConnect()

        assertEquals(DeviceLinkPhase.Collecting, s.run.phase)
        assertEquals(emptyList<DeviceLinkLogLine>(), s.run.log)
        
        assertEquals(listOf("peer-1"), s.linkedDevices.value.map { it.peerId })
        assertNotNull(s.destroyGuard.value)
    }

    @Test
    fun aReconnectWithTheGuardSetChecksTheOtherDeviceBeforeSayingSo() = runTest {
        val daemon = FakeDaemon()
            .answer(FrameType.DeviceLinkOffer, started("h-1", 5_000L))
            .answer(FrameType.DeviceLinksList, devicesListed("peer-1"))
            .answer(FrameType.GetDestroyPin, guardListed(set = true, siblingWithSalt = "peer-1"))
            .answer(FrameType.VerifyDestroyPin, verified(answered = true, verified = true))
        val s = store(daemon)
        s.offeringRun()

        s.reconcileDeviceLinkAfterConnect()

        assertEquals(DeviceLinkPhase.Done, s.run.phase)
        assertFalse(s.run.failed)
        assertEquals(CEREMONY_DONE, s.run.lastLine)
        
        
        assertEquals(0, daemon.countOf(FrameType.SetDestroyPin))
        
        assertEquals(1, daemon.countOf(FrameType.VerifyDestroyPin))
    }

    
    @Test
    fun aReconnectWithNoPinHeldDoesNotClaimTheOtherPinWasChecked() = runTest {
        val daemon = FakeDaemon()
            .answer(FrameType.DeviceLinkOffer, started("h-1", 5_000L))
            .answer(FrameType.DeviceLinksList, devicesListed("peer-1"))
            .answer(FrameType.GetDestroyPin, guardListed(set = true, siblingWithSalt = "peer-1"))
            .answer(FrameType.VerifyDestroyPin, verified(answered = true, verified = true))
        val s = store(daemon)
        s.offeringRun()
        s._deviceLinkRun.value = s.run.copy(otherPin = "")

        s.reconcileDeviceLinkAfterConnect()

        assertEquals(DeviceLinkPhase.Done, s.run.phase)
        assertFalse(s.run.failed)
        assertEquals(CEREMONY_LINKED_UNCHECKED, s.run.lastLine)
        assertEquals(0, daemon.countOf(FrameType.VerifyDestroyPin))
        assertEquals(0, daemon.countOf(FrameType.SetDestroyPin))
    }

    
    @Test
    fun guardOnlyRunsTheCeremonyWithTheLinkTakenOut() = runTest {
        val daemon = FakeDaemon()
            .answer(FrameType.SetDestroyPin, pinSet(true))
            .answer(FrameType.GetDestroyPin, guardListed(set = false, siblingWithSalt = "peer-1"))
            .answer(FrameType.VerifyDestroyPin, verified(answered = true, verified = true))
        val s = store(daemon)
        s._linkedDevices.value = listOf(DeviceLinkEntry(peerId = "peer-1", linkedAt = 0L))
        s._destroyGuard.value = DestroyPinListedResponse(set = false, setAt = 0L, siblings = emptyList())
        assertTrue(s.guardOnlyIsOffered())

        s.beginDeviceLinkRun(DeviceLinkMode.GuardOnly)
        s.setDeviceLinkPins("1111", "2222")
        s.proceedDeviceLink()

        assertEquals(CEREMONY_DONE, s.run.lastLine)
        assertEquals(0, daemon.countOf(FrameType.DeviceLinkOffer))
        assertEquals(0, daemon.countOf(FrameType.DeviceLinkAccept))
    }

    @Test
    fun endingARunDropsThePins() = runTest {
        val daemon = FakeDaemon().answer(FrameType.DeviceLinkOffer, started("h-1", 5_000L))
        val s = store(daemon)
        s.offeringRun()
        assertEquals("1111", s.run.myPin)

        s.endDeviceLinkRun()

        assertNull("a run left behind keeps its PINs in memory", s._deviceLinkRun.value)
    }

    
    private fun MessengerStore.closeAndWait(): LinkedDevicesView {
        val closed = _deviceLinkRun.value!!
        endDeviceLinkRun()
        assertEquals("the PINs stayed after Close", "", _deviceLinkRun.value?.myPin.orEmpty())
        
        val deadline = System.currentTimeMillis() + 5_000L
        while (System.currentTimeMillis() < deadline) {
            val cur = _deviceLinkRun.value
            assertTrue(
                "the link form showed while Close was reading the linked devices",
                linkedDevicesView(cur, linkedDevices.value, null) != LinkedDevicesView.Form,
            )
            if (cur == null || cur.id != closed.id || !cur.closing) break
            Thread.sleep(1L)
        }
        return linkedDevicesView(_deviceLinkRun.value, linkedDevices.value, destroyGuard.value?.waiting)
    }

    @Test
    fun closingARunThatLinkedLandsOnTheLinkedView() = runTest {
        
        
        val daemon = FakeDaemon()
            .answer(FrameType.DeviceLinkOffer, started("h-1", 5_000L))
            .answer(FrameType.SetDestroyPin, pinSet(true))
            .answer(FrameType.GetDestroyPin, guardListed(set = true, siblingWithSalt = "peer-1"))
            .answer(FrameType.VerifyDestroyPin, verified(answered = true, verified = true))
            .answer(FrameType.DeviceLinksList, devicesListed("peer-1"))
        val s = store(daemon)
        s.offeringRun()
        s.takeDeviceLinkCompletion(DeviceLinkCompletedPush(handleId = "h-1", peerId = "peer-1"))
        assertEquals(CEREMONY_DONE, s.run.lastLine)
        assertTrue("the fixture read the linked devices during the run", s.linkedDevices.value.isEmpty())

        assertEquals("Close after a link drew the wrong view", LinkedDevicesView.Linked, s.closeAndWait())
    }

    @Test
    fun closingAJoinThatLinkedLandsOnTheLinkedView() = runTest {
        
        val daemon = FakeDaemon()
            .answer(FrameType.DeviceLinkAccept, accepted("peer-1"))
            .answer(FrameType.SetDestroyPin, pinSet(true))
            .answer(FrameType.GetDestroyPin, guardListed(set = true, siblingWithSalt = "peer-1"))
            .answer(FrameType.VerifyDestroyPin, verified(answered = true, verified = true))
            .answer(FrameType.DeviceLinksList, devicesListed("peer-1"))
        val s = store(daemon)
        s.beginDeviceLinkRun(DeviceLinkMode.Joining)
        s.setDeviceLinkPins("1111", "2222")
        s.setDeviceLinkWords("a b c d e f g")
        s.proceedDeviceLink()
        assertEquals(CEREMONY_DONE, s.run.lastLine)
        assertTrue("the fixture read the linked devices during the run", s.linkedDevices.value.isEmpty())

        assertEquals("Close after a join drew the wrong view", LinkedDevicesView.Linked, s.closeAndWait())
    }

    @Test
    fun aCloseThatCannotReadTheLinkedDevicesKeepsTheRunAndCanBeRepeated() = runTest {
        val daemon = FakeDaemon()
            .answer(FrameType.DeviceLinkOffer, started("h-1", 5_000L))
            .answer(FrameType.SetDestroyPin, pinSet(true))
            .answer(FrameType.GetDestroyPin, guardListed(set = true, siblingWithSalt = "peer-1"))
            .answer(FrameType.VerifyDestroyPin, verified(answered = true, verified = true))
        val s = MessengerStore(
            requestSender = { type, payload -> daemon.handle(type, payload) },
            deviceLinkTimings = DeviceLinkTimings(saltWaitMs = 100L, saltPollMs = 10L, closeRetryMs = 1L),
        ).also { it._connection.value = true }
        s.offeringRun()
        s.takeDeviceLinkCompletion(DeviceLinkCompletedPush(handleId = "h-1", peerId = "peer-1"))

        
        assertEquals(LinkedDevicesView.Run, s.closeAndWait())
        assertEquals(LINKED_DEVICES_NOT_READ, s.run.lastLine)
        assertEquals("the read was not tried again", 2, daemon.countOf(FrameType.DeviceLinksList))

        daemon.answer(FrameType.DeviceLinksList, devicesListed("peer-1"))
        assertEquals(LinkedDevicesView.Linked, s.closeAndWait())
    }
}
