package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.Frame
import io.haoma.calculator.core.ipc.FrameType
import kotlin.time.Duration
import kotlin.time.Duration.Companion.seconds
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.delay
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.withTimeout
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test


@OptIn(ExperimentalCoroutinesApi::class)
class JoinWaitTest {

    
    private data class Asked(val type: String, val timeout: Duration)

    
    private class FakeDaemon(
        val answer: suspend (String) -> Frame,
        val during: () -> Unit = {},
    ) {
        val asked = mutableListOf<Asked>()

        suspend fun handle(type: String, timeout: Duration): Frame {
            asked.add(Asked(type, timeout))
            during()
            return answer(type)
        }
    }

    private val clockMs = 5_000_000L

    
    private fun store(daemon: FakeDaemon) = MessengerStore(
        timedRequestSender = { type, _, timeout -> daemon.handle(type, timeout) },
        deviceLinkTimings = DeviceLinkTimings(
            saltWaitMs = 100L,
            saltPollMs = 10L,
            nowMs = { clockMs },
        ),
    ).also { it._connection.value = true }

    private fun refusal(code: String) = Frame(
        type = FrameType.Error,
        payload = JSONObject().put("code", code).put("message", "the rendezvous did not answer"),
    )

    private val words = "acid acorn acre acts afar affix aged"

    private val MessengerStore.run: DeviceLinkRun get() = _deviceLinkRun.value!!

    
    private fun MessengerStore.joiningRun() {
        beginDeviceLinkRun(DeviceLinkMode.Joining)
        setDeviceLinkPins("1111", "2222")
        setDeviceLinkWords(words)
    }

    
    @Test
    fun theCountStartsFullAndReadsOneInItsLastSecond() {
        val start = 1_000L
        assertEquals(JOINER_DIAL_SECONDS, joinSecondsLeft(start, start))
        assertEquals(JOINER_DIAL_SECONDS, joinSecondsLeft(start, start + 1L))
        assertEquals(1L, joinSecondsLeft(start, start + JOINER_DIAL_SECONDS * 1000L - 500L))
        assertEquals(0L, joinSecondsLeft(start, start + JOINER_DIAL_SECONDS * 1000L))
        assertEquals(0L, joinSecondsLeft(start, start + 3_600_000L))

        assertEquals("Waiting for Tor: 120 seconds left.", joinWaitLine(start, start))
        assertEquals("Waiting for Tor: 1 second left.", joinWaitLine(start, start + 119_000L))
        assertEquals(JOIN_WAIT_NO_ANSWER, joinWaitLine(start, start + 120_000L))
    }

    @Test
    fun onlyADialThatDidNotGetThroughIsToldToTryLater() {
        assertTrue(joinDialFailed("onion_accept_failed"))
        assertTrue(joinDialFailed("backend_unavailable"))
        for (code in listOf("", "bad_request", "not_ready", "device_not_empty", "already_linked", "no_answer")) {
            assertFalse("$code was read as a slow dial", joinDialFailed(code))
        }
    }

    
    @Test
    fun theJoinWaitsLongerThanEverythingHaomaWaitsFor() {
        val dial = JOINER_DIAL_SECONDS.seconds
        assertTrue("a pairing join gives up before the dial does", PAIR_ONION_ACCEPT_TIMEOUT > dial)
        val haoma = (JOINER_DIAL_SECONDS + DEVICE_LINK_REGISTER_SECONDS + DEVICE_LINK_BUNDLE_SECONDS).seconds
        assertTrue("a link join gives up before haoma answers", DEVICE_LINK_ACCEPT_TIMEOUT > haoma)
    }

    
    @Test
    fun aLinkJoinCountsDownAndAsksWithTheLongTimeout() = runTest {
        var dialSeenAt = -1L
        lateinit var s: MessengerStore
        val daemon = FakeDaemon(
            answer = { refusal("backend_unavailable") },
            during = { dialSeenAt = s.run.dialStartedAtMs },
        )
        s = store(daemon)
        s.joiningRun()
        s.proceedDeviceLink()

        assertEquals("the run did not hold the countdown while the join waited", clockMs, dialSeenAt)
        assertEquals(0L, s.run.dialStartedAtMs)
        assertEquals(listOf(Asked(FrameType.DeviceLinkAccept, DEVICE_LINK_ACCEPT_TIMEOUT)), daemon.asked)
    }

    @Test
    fun aLinkJoinThatTorDidNotCarryKeepsTheForm() = runTest {
        val daemon = FakeDaemon(answer = { refusal("backend_unavailable") })
        val s = store(daemon)
        s.joiningRun()
        s.proceedDeviceLink()

        assertEquals("the run did not go back to its form", DeviceLinkPhase.Collecting, s.run.phase)
        assertEquals("1111", s.run.myPin)
        assertEquals("2222", s.run.otherPin)
        assertEquals(words, s.run.wordsInput)
        assertFalse("a join that can be tried again was marked as ended badly", s.run.failed)
        assertFalse("the spinner is still turning", s.run.busy)
        assertEquals(JOIN_TOR_SLOW, s.run.log.last().text)
        assertTrue(s.run.log.last().aside)
        assertTrue("the Tor slow line is not drawn orange", s.run.log.last().torSlow)
        assertEquals(DeviceLinkOutcome.Failed, s.run.log.last { !it.aside }.outcome)

        
        s.proceedDeviceLink()
        assertEquals(2, daemon.asked.count { it.type == FrameType.DeviceLinkAccept })
    }

    
    @Test
    fun aLinkJoinRefusedForAnotherReasonEndsTheRun() = runTest {
        val daemon = FakeDaemon(answer = { refusal("device_not_empty") })
        val s = store(daemon)
        s.joiningRun()
        s.proceedDeviceLink()

        assertEquals(DeviceLinkPhase.Done, s.run.phase)
        assertTrue(s.run.failed)
        assertFalse(s.run.log.any { it.text == JOIN_TOR_SLOW })
    }

    @Test
    fun aLinkJoinWithAWrongWordKeepsTheFormAndSaysSo() = runTest {
        val s = store(FakeDaemon(answer = { refusal(REASON_JOIN_WORDS_INVALID) }))
        s.joiningRun()
        s.proceedDeviceLink()
        assertEquals(DeviceLinkPhase.Collecting, s.run.phase)
        assertEquals(words, s.run.wordsInput)
        assertEquals(JOIN_WORDS_WRONG, s.run.log.last().text)
    }

    @Test
    fun aLinkJoinOnASpentOfferKeepsTheFormAndAsksForANewOne() = runTest {
        val s = store(FakeDaemon(answer = { refusal(REASON_JOIN_INVITATION_SPENT) }))
        s.joiningRun()
        s.proceedDeviceLink()
        assertEquals(DeviceLinkPhase.Collecting, s.run.phase)
        assertEquals("1111", s.run.myPin)
        assertEquals(JOIN_INVITATION_SPENT, s.run.log.last().text)
    }

    @Test
    fun aLinkJoinWhoseWordsDoNotLookValidIsNotSent() = runTest {
        val daemon = FakeDaemon(answer = { refusal("") })
        val s = store(daemon)
        s.joiningRun()
        s.proceedDeviceLink(wordsLookValid = false)
        assertEquals(DeviceLinkPhase.Collecting, s.run.phase)
        assertEquals(JOIN_WORDS_WRONG, s.run.log.last().text)
        assertTrue("a join went out with words the screen refused", daemon.asked.isEmpty())
    }

    
    @Test
    fun aLinkJoinThatGotNoAnswerSaysItMayHaveGoneThrough() = runTest {
        lateinit var s: MessengerStore
        val daemon = FakeDaemon(answer = { s._connection.value = false; delay(10_000L); refusal("") })
        s = store(daemon)
        s.joiningRun()
        s.proceedDeviceLink()
        assertEquals(DeviceLinkPhase.Done, s.run.phase)
        assertTrue(s.run.log.any { it.text == "The link may have gone through." })
    }

    
    @Test
    fun aLateNoAnswerLeavesARunThatHasMovedOn() = runTest {
        lateinit var s: MessengerStore
        val daemon = FakeDaemon(
            answer = {
                s._deviceLinkRun.value = s.run.copy(peerId = "peer-1", guardStepsStarted = true)
                s._connection.value = false
                delay(10_000L)
                refusal("")
            },
        )
        s = store(daemon)
        s.joiningRun()
        s.proceedDeviceLink()
        assertEquals(DeviceLinkPhase.Working, s.run.phase)
        assertFalse(s.run.failed)
    }

    
    @Test
    fun aRunPastTheJoinIsNotSentBackToItsForm() = runTest {
        val s = store(FakeDaemon(answer = { refusal("") }))
        s.joiningRun()
        s._deviceLinkRun.value = s.run.copy(phase = DeviceLinkPhase.Working, peerId = "peer-1")
        assertFalse(s.retryDeviceLinkJoinLater(s.run.id))
        assertEquals(DeviceLinkPhase.Working, s.run.phase)

        s._deviceLinkRun.value = s.run.copy(peerId = "", guardStepsStarted = true)
        assertFalse(s.retryDeviceLinkJoinLater(s.run.id))
        assertEquals(DeviceLinkPhase.Working, s.run.phase)
    }

    
    @Test
    fun theProceedClaimIsTakenOnce() {
        val s = store(FakeDaemon(answer = { refusal("") }))
        s.joiningRun()
        val id = s.run.id
        assertTrue(s.claimDeviceLinkProceed(id, "1111", "2222", splitWords(words)))
        assertFalse("a second press started the run again", s.claimDeviceLinkProceed(id, "1111", "2222", splitWords(words)))
    }

    
    @Test
    fun aSecondTapOnTheFormDoesNotStartASecondRun() {
        val s = store(FakeDaemon(answer = { refusal("") }))
        s.updateDeviceLinkDraft { it.copy(myPin = "1111", otherPin = "2222", words = words) }
        val first = s.beginDeviceLinkRunFromDraft(DeviceLinkMode.Joining)
        val second = s.beginDeviceLinkRunFromDraft(DeviceLinkMode.Joining)
        assertTrue(first != null)
        assertEquals(null, second)
        assertEquals(first, s.run.id)
        assertEquals("1111", s.run.myPin)
    }

    
    private fun MessengerStore.typeEverything() {
        updateDeviceLinkDraft { it.copy(joinTab = true, myPin = "1111", otherPin = "2222", words = words) }
        setPairJoinWords(words)
    }

    @Test
    fun aSoftLockDropsTheDraftAndThePairingWords() {
        val s = store(FakeDaemon(answer = { refusal("") }))
        s.typeEverything()
        s.syncLockState(true)
        assertEquals(DeviceLinkDraft(), s.deviceLinkDraft.value)
        assertEquals(PairJoinState(), s.pairJoin.value)
    }

    @Test
    fun stoppingTheDaemonsDropsTheDraftAndThePairingWords() {
        val s = store(FakeDaemon(answer = { refusal("") }))
        s.typeEverything()
        s.onDaemonsStopped()
        assertEquals(DeviceLinkDraft(), s.deviceLinkDraft.value)
        assertEquals(PairJoinState(), s.pairJoin.value)
    }

    @Test
    fun aLockEndsACollectingRunAndBlanksAWorkingOne() {
        val s = store(FakeDaemon(answer = { refusal("") }))
        s.joiningRun()
        s.syncLockState(true)
        assertEquals("a run still collecting outlived the lock", null, s._deviceLinkRun.value)

        s.syncLockState(false)
        s.joiningRun()
        s._deviceLinkRun.value = s.run.copy(phase = DeviceLinkPhase.Working, words = splitWords(words))
        s.onDaemonsStopped()
        assertEquals("", s.run.myPin)
        assertEquals("", s.run.otherPin)
        assertEquals("", s.run.wordsInput)
        assertTrue(s.run.words.isEmpty())
    }

    
    @Test
    fun aLockDuringAPairingJoinKeepsTheWait() {
        val s = store(FakeDaemon(answer = { refusal("") }))
        s._pairJoin.value = PairJoinState(wordsInput = words, waitingSinceMs = clockMs)
        s.syncLockState(true)
        assertEquals(PairJoinState(waitingSinceMs = clockMs), s.pairJoin.value)
    }

    @Test
    fun theLinkFormDraftIsHandedToTheRunOnce() {
        val s = store(FakeDaemon(answer = { refusal("") }))
        s.updateDeviceLinkDraft { it.copy(joinTab = true, myPin = "1111") }
        s.updateDeviceLinkDraft { it.copy(otherPin = "2222", words = words) }

        s.beginDeviceLinkRunFromDraft(DeviceLinkMode.Joining)

        assertEquals("1111", s.run.myPin)
        assertEquals("2222", s.run.otherPin)
        assertEquals(words, s.run.wordsInput)
        
        assertEquals(DeviceLinkDraft(joinTab = true), s.deviceLinkDraft.value)
    }

    
    @Test
    fun aPairingJoinCountsDownAndAsksWithTheLongTimeout() = runTest {
        var waitingSeen = -1L
        lateinit var s: MessengerStore
        val daemon = FakeDaemon(
            answer = { refusal("onion_accept_failed") },
            during = { waitingSeen = s.pairJoin.value.waitingSinceMs },
        )
        s = store(daemon)
        s.setPairJoinWords(words)
        s.runPairJoin(nowMs = { clockMs })

        assertEquals("the form did not hold the countdown while the join waited", clockMs, waitingSeen)
        assertEquals(0L, s.pairJoin.value.waitingSinceMs)
        assertEquals(listOf(Asked(FrameType.PairOnionAccept, PAIR_ONION_ACCEPT_TIMEOUT)), daemon.asked)
    }

    @Test
    fun aPairingJoinThatTorDidNotCarryKeepsTheWords() = runTest {
        val s = store(FakeDaemon(answer = { refusal("onion_accept_failed") }))
        s.setPairJoinWords(words)
        s.runPairJoin(nowMs = { clockMs })

        assertEquals(PairJoinState(wordsInput = words, error = JOIN_TOR_SLOW), s.pairJoin.value)
    }

    
    @Test
    fun aPairingJoinThisSideStoppedWaitingForSaysTheOutcomeIsUnknown() = runTest {
        
        val s = store(FakeDaemon(answer = { withTimeout(1L) { delay(1_000L) }; refusal("") }))
        s.setPairJoinWords(words)
        s.runPairJoin(nowMs = { clockMs })

        assertEquals(PairJoinState(wordsInput = words, error = PAIR_JOIN_NO_ANSWER), s.pairJoin.value)
    }

    
    @Test
    fun aPairingJoinWhoseConnectionDroppedSaysTheOutcomeIsUnknown() = runTest {
        lateinit var s: MessengerStore
        val daemon = FakeDaemon(answer = { s._connection.value = false; delay(10_000L); refusal("") })
        s = store(daemon)
        s.setPairJoinWords(words)
        s.runPairJoin(nowMs = { clockMs })

        assertEquals(PAIR_JOIN_NO_ANSWER, s.pairJoin.value.error)
    }

    @Test
    fun aPairingJoinWithAWrongWordSaysSoAndKeepsTheWords() = runTest {
        val s = store(FakeDaemon(answer = { refusal(REASON_JOIN_WORDS_INVALID) }))
        s.setPairJoinWords(words)
        s.runPairJoin(nowMs = { clockMs })
        assertEquals(PairJoinState(wordsInput = words, error = JOIN_WORDS_WRONG), s.pairJoin.value)
    }

    
    @Test
    fun aPairingJoinWhoseWordsDoNotLookValidIsNotSent() = runTest {
        val daemon = FakeDaemon(answer = { refusal("") })
        val s = store(daemon)
        s.setPairJoinWords(words)
        s.runPairJoin(wordsLookValid = false, nowMs = { clockMs })
        assertEquals(PairJoinState(wordsInput = words, error = JOIN_WORDS_WRONG), s.pairJoin.value)
        assertTrue("a join went out with words the screen refused", daemon.asked.isEmpty())
    }

    @Test
    fun aPairingJoinOnASpentInvitationAsksForANewOne() = runTest {
        val s = store(FakeDaemon(answer = { refusal(REASON_JOIN_INVITATION_SPENT) }))
        s.setPairJoinWords(words)
        s.runPairJoin(nowMs = { clockMs })
        assertEquals(PairJoinState(wordsInput = "", error = JOIN_INVITATION_SPENT), s.pairJoin.value)
    }

    @Test
    fun aPairingJoinThatWentThroughEmptiesTheForm() = runTest {
        val accepted = Frame(
            type = FrameType.PairOnionAccepted,
            payload = JSONObject().put("peer_id", "peer-1").put("nick", "bob"),
        )
        val s = store(FakeDaemon(answer = { accepted }))
        s.setPairJoinWords(words)
        s.runPairJoin(nowMs = { clockMs })

        assertEquals(PairJoinState(), s.pairJoin.value)
    }

    
    @Test
    fun aPairingJoinRefusedForAnotherReasonSaysSo() = runTest {
        val s = store(FakeDaemon(answer = { refusal("bad_invite") }))
        s.setPairJoinWords(words)
        s.runPairJoin(nowMs = { clockMs })

        assertEquals(words, s.pairJoin.value.wordsInput)
        assertFalse(s.pairJoin.value.error == JOIN_TOR_SLOW)
        assertTrue(s.pairJoin.value.error.isNotEmpty())
    }
}
