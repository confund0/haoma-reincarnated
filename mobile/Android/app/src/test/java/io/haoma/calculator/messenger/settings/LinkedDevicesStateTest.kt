package io.haoma.calculator.messenger.settings

import io.haoma.calculator.core.ipc.Frame
import io.haoma.calculator.core.ipc.FrameType
import io.haoma.calculator.messenger.DestroyPinListedResponse
import io.haoma.calculator.messenger.DestroyPinSiblingEntry
import io.haoma.calculator.messenger.DestroyPinVerifiedReason
import io.haoma.calculator.messenger.DestroyPinVerifiedResponse
import io.haoma.calculator.messenger.DeviceLinkEntry
import io.haoma.calculator.messenger.DeviceLinkLogLine
import io.haoma.calculator.messenger.DestroyWaiting
import io.haoma.calculator.messenger.DeviceLinkMode
import io.haoma.calculator.messenger.DeviceLinkOutcome
import io.haoma.calculator.messenger.DeviceLinkRun
import io.haoma.calculator.messenger.DeviceUnlinkedResponse
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.UNLINK_DONE
import io.haoma.calculator.messenger.VERIFY_ACCEPTED
import io.haoma.calculator.messenger.VERIFY_ANSWER_BAD
import io.haoma.calculator.messenger.VERIFY_NOT_SENT
import io.haoma.calculator.messenger.VERIFY_NO_ANSWER
import io.haoma.calculator.messenger.VERIFY_REFUSED
import io.haoma.calculator.messenger.destroyLinkedDevice
import io.haoma.calculator.messenger.guardOnlyIsOffered
import io.haoma.calculator.messenger.unlinkLinkedDevice
import io.haoma.calculator.messenger.unlinkOutcome
import io.haoma.calculator.messenger.verifyOtherDestroyPin
import io.haoma.calculator.messenger.verifyOutcome
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.runTest
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test


class LinkedDevicesStateTest {

    private fun device(peerId: String = "peer-1", linkedAt: Long = 100L) =
        DeviceLinkEntry(peerId = peerId, linkedAt = linkedAt)

    private fun guard(set: Boolean, saltFor: String? = null) = DestroyPinListedResponse(
        set = set,
        setAt = if (set) 100L else 0L,
        siblings = if (saltFor == null) {
            emptyList()
        } else {
            listOf(DestroyPinSiblingEntry(peerId = saltFor, saltHeld = true, receivedAt = 100L))
        },
    )

    
    @Test
    fun anEmptyDeviceWithNoRunShowsTheTabs() {
        assertEquals(LinkedDevicesView.Form, linkedDevicesView(null, emptyList()))
    }

    @Test
    fun aRunReplacesTheTabs() {
        val run = DeviceLinkRun(id = 1L, mode = DeviceLinkMode.Inviting)
        assertEquals(LinkedDevicesView.Run, linkedDevicesView(run, emptyList()))
    }

    @Test
    fun endingARunThatLinkedNothingBringsTheTabsBack() {
        val run = DeviceLinkRun(id = 1L, mode = DeviceLinkMode.Inviting)
        assertEquals(LinkedDevicesView.Run, linkedDevicesView(run, emptyList()))
        assertEquals(LinkedDevicesView.Form, linkedDevicesView(null, emptyList()))
    }

    @Test
    fun aFinishedLinkResolvesToTheLinkedViewAndNotTheForm() {
        val devices = listOf(device())
        assertEquals(
            "a link that came up must not drop the person back on the form that started it",
            LinkedDevicesView.Linked,
            linkedDevicesView(null, devices),
        )
    }

    @Test
    fun aRunStaysOnScreenWhileALinkedDeviceExists() {
        
        
        val run = DeviceLinkRun(id = 1L, mode = DeviceLinkMode.Inviting)
        assertEquals(LinkedDevicesView.Run, linkedDevicesView(run, listOf(device())))
    }

    
    @Test
    fun aDeviceWithContactsIsNotOfferedTheJoinTab() {
        assertFalse("the daemon refuses this join, so it is never offered", joinTabIsOffered(1, 0))
    }

    @Test
    fun aDeviceWithChatsIsNotOfferedTheJoinTab() {
        assertFalse(joinTabIsOffered(0, 1))
    }

    @Test
    fun aCleanDeviceIsOfferedTheJoinTab() {
        assertTrue(joinTabIsOffered(0, 0))
    }

    @Test
    fun theScreenSaysWhyJoiningIsNotOffered() {
        assertTrue(
            "an action that is simply missing reads as a fault",
            JOIN_NOT_OFFERED.contains("cannot join"),
        )
    }

    
    @Test
    fun aLinkedDeviceWithNoGuardIsOfferedToSetOne() {
        val s = MessengerStore()
        s._linkedDevices.value = listOf(device())
        s._destroyGuard.value = guard(set = false)
        assertTrue(s.guardOnlyIsOffered())
    }

    @Test
    fun aLinkedDeviceWithAGuardIsNotOfferedToSetOne() {
        val s = MessengerStore()
        s._linkedDevices.value = listOf(device())
        s._destroyGuard.value = guard(set = true, saltFor = "peer-1")
        assertFalse(s.guardOnlyIsOffered())
    }

    @Test
    fun anUnlinkedDeviceIsNotOfferedToSetOne() {
        val s = MessengerStore()
        s._destroyGuard.value = guard(set = false)
        assertFalse("nobody could ever use a guard here", s.guardOnlyIsOffered())
    }

    
    @Test
    fun withoutTheOtherDevicesSaltNoCheckCanBeAttempted() {
        val card = linkedDeviceCard(device(), guard(set = true))
        assertTrue(card.guardHeld)
        assertFalse(card.saltHeld)
        assertFalse("the salt is what the question is built from", card.canCheckOtherPin)
    }

    @Test
    fun withTheOtherDevicesSaltACheckCanBeAttempted() {
        val card = linkedDeviceCard(device(), guard(set = true, saltFor = "peer-1"))
        assertTrue(card.saltHeld)
        assertTrue(card.canCheckOtherPin)
    }

    @Test
    fun aSaltHeldForSomeOtherDeviceDoesNotCount() {
        val card = linkedDeviceCard(device(), guard(set = true, saltFor = "peer-2"))
        assertFalse(card.saltHeld)
        assertFalse(card.canCheckOtherPin)
    }

    @Test
    fun noGuardStateAtAllReadsAsNothingHeld() {
        val card = linkedDeviceCard(device(), null)
        assertFalse(card.guardHeld)
        assertFalse(card.canCheckOtherPin)
    }

    
    private fun runWithLog(busy: Boolean, vararg lines: DeviceLinkLogLine) =
        DeviceLinkRun(id = 1L, mode = DeviceLinkMode.Inviting, log = lines.toList(), busy = busy)

    @Test
    fun aWorkingRunDrawsExactlyOneSpinner() {
        val rows = deviceLinkLogRows(
            runWithLog(
                busy = true,
                DeviceLinkLogLine("first", outcome = DeviceLinkOutcome.Done),
                DeviceLinkLogLine("an aside", aside = true),
                DeviceLinkLogLine("second"),
            ),
        )
        assertEquals(1, rows.count { it.spinner })
        assertTrue("the spinner sits on the newest step", rows.last().spinner)
    }

    @Test
    fun aFinishedRunDrawsNoSpinner() {
        val rows = deviceLinkLogRows(
            runWithLog(
                busy = false,
                DeviceLinkLogLine("first", outcome = DeviceLinkOutcome.Failed),
                DeviceLinkLogLine("what to do next", aside = true),
            ),
        )
        assertEquals(0, rows.count { it.spinner })
    }

    @Test
    fun anAsideNeverSpinsAndNeverCarriesAMarker() {
        val rows = deviceLinkLogRows(
            runWithLog(
                busy = true,
                DeviceLinkLogLine("a step"),
                DeviceLinkLogLine("what it means", aside = true),
            ),
        )
        val aside = rows.last()
        assertTrue(aside.aside)
        assertFalse("an aside that spins turns a finished run back into a working one", aside.spinner)
        assertEquals(LOG_MARKER_NONE, aside.marker)
        assertTrue("the spinner belongs to the step above", rows.first().spinner)
    }

    @Test
    fun anOutcomeIsDrawnOnTheLineThatEndedIt() {
        val rows = deviceLinkLogRows(
            runWithLog(
                busy = false,
                DeviceLinkLogLine("it worked", outcome = DeviceLinkOutcome.Done),
                DeviceLinkLogLine("it did not", outcome = DeviceLinkOutcome.Failed),
                DeviceLinkLogLine("nobody answered", outcome = DeviceLinkOutcome.Warning),
            ),
        )
        assertEquals(LOG_MARKER_DONE, rows[0].marker)
        assertEquals(LOG_MARKER_FAILED, rows[1].marker)
        
        assertEquals(LOG_MARKER_WARNING, rows[2].marker)
    }

    @Test
    fun theLogIsDrawnOldestFirst() {
        val rows = deviceLinkLogRows(
            runWithLog(busy = false, DeviceLinkLogLine("older"), DeviceLinkLogLine("newer")),
        )
        assertEquals(listOf("older", "newer"), rows.map { it.text })
    }

    
    @Test
    fun aCheckThatNeverWentOutSaysSo() {
        assertEquals(VERIFY_NOT_SENT, verifyOutcome(null).text)
        assertFalse(verifyOutcome(null).ok)
    }

    @Test
    fun anUnansweredCheckIsNotARefusal() {
        val out = verifyOutcome(DestroyPinVerifiedResponse(answered = false, verified = false, reason = ""))
        assertEquals(VERIFY_NO_ANSWER, out.text)
        assertFalse(out.ok)
    }

    
    @Test
    fun aReplyThatDidNotCheckOutIsItsOwnSentence() {
        val out = verifyOutcome(
            DestroyPinVerifiedResponse(answered = false, verified = false, reason = DestroyPinVerifiedReason.ANSWER_DID_NOT_CHECK_OUT),
        )
        assertEquals(VERIFY_ANSWER_BAD, out.text)
        assertFalse(out.ok)
    }

    @Test
    fun aRefusedCheckIsNotSilence() {
        val out = verifyOutcome(DestroyPinVerifiedResponse(answered = true, verified = false, reason = ""))
        assertEquals(VERIFY_REFUSED, out.text)
        assertFalse(out.ok)
    }

    @Test
    fun anAcceptedCheckSaysOnlyThatThePinWasAccepted() {
        val out = verifyOutcome(DestroyPinVerifiedResponse(answered = true, verified = true, reason = ""))
        assertEquals(VERIFY_ACCEPTED, out.text)
        assertTrue(out.ok)
        assertFalse("a check on its own links nothing", out.text.contains("linked"))
    }

    
    private fun unlinked(sent: Boolean = false, receipted: Boolean = false, reason: String = "") =
        DeviceUnlinkedResponse(
            unlinked = true,
            sent = sent,
            receipted = receipted,
            deadline = 0L,
            reason = reason,
        )

    @Test
    fun anUnlinkSaysOnlyThatThisDeviceForgotTheOtherOne() {
        val out = unlinkOutcome(unlinked(), wantedDestroy = false)
        assertTrue(out.ok)
        assertEquals(UNLINK_DONE, out.text)
        assertFalse("unlink erases nothing", out.text.contains("eras"))
    }

    @Test
    fun aReceiptedDestroyIsNeverPresentedAsProofOfErasure() {
        val out = unlinkOutcome(unlinked(sent = true, receipted = true), wantedDestroy = true)
        assertTrue(out.ok)
        assertTrue(
            "the other device signs its own receipt, so the line says it answered and nothing more",
            out.text.contains("answered that"),
        )
    }

    @Test
    fun aDestroyThatWentOutWithNoAnswerClaimsNothing() {
        val out = unlinkOutcome(unlinked(sent = true), wantedDestroy = true)
        assertTrue(out.ok)
        assertFalse(out.text.contains("erased"))
        assertTrue(out.text.contains("no answer came back"))
    }

    @Test
    fun aDestroyToADeviceThatCouldNotBeReachedCommandsNothing() {
        val out = unlinkOutcome(unlinked(), wantedDestroy = true)
        assertTrue(out.ok)
        assertFalse(out.text.contains("erased"))
        assertTrue(out.text.contains("could not be reached"))
    }

    @Test
    fun noDestroyOutcomeEverStatesTheOtherDeviceWasErased() {
        val everyOutcome = listOf(
            unlinkOutcome(unlinked(sent = true, receipted = true), wantedDestroy = true),
            unlinkOutcome(unlinked(sent = true), wantedDestroy = true),
            unlinkOutcome(unlinked(), wantedDestroy = true),
            unlinkOutcome(unlinked(), wantedDestroy = false),
        )
        everyOutcome.forEach { out ->
            assertFalse(
                "no line may present a receipt as proof: ${out.text}",
                out.text.contains("That device erased itself") ||
                    out.text.contains("was erased") ||
                    out.text.contains("has erased"),
            )
        }
    }

    @Test
    fun aRefusedDestroyChangesNothingAndSaysSo() {
        val out = unlinkOutcome(
            DeviceUnlinkedResponse(
                unlinked = false,
                sent = false,
                receipted = false,
                deadline = 0L,
                reason = "wrong_destroy_pin",
            ),
            wantedDestroy = true,
        )
        assertFalse(out.ok)
        assertTrue(out.text.contains("Nothing was changed"))
    }

    @Test
    fun anAnswerThatCouldNotBeReadIsNotSuccess() {
        val out = unlinkOutcome(null, wantedDestroy = false)
        assertFalse(out.ok)
    }

    
    @Test
    fun theUnlinkConfirmationNamesTheDeviceAndSaysItKeepsItsData() {
        val text = unlinkConfirmText("peer-abcdefgh")
        assertTrue(text.contains(shortPeerId("peer-abcdefgh")))
        assertTrue(text.contains("keeps"))
        assertTrue(text.contains("cannot be undone"))
    }

    @Test
    fun theDestroyConfirmationDoesNotClaimTheOtherDeviceWasErased() {
        val text = destroyConfirmText("peer-abcdefgh")
        assertTrue(text.contains("is told to erase"))
        assertFalse("nothing here can know that", text.contains("was erased"))
        assertFalse(text.contains("has erased"))
    }

    @Test
    fun theDestroyConfirmationSaysWhatFailsSilently() {
        val text = destroyConfirmText("peer-abcdefgh")
        assertTrue(text.contains("switched off"))
        assertTrue(text.contains("wrong PIN"))
    }

    @Test
    fun theTwoConfirmationsAreNotTheSameSentence() {
        assertFalse(
            "unlink and destroy are two actions and the screen names the difference",
            unlinkConfirmText("peer-1") == destroyConfirmText("peer-1"),
        )
    }

    
    private class Recorder {
        val sent = mutableListOf<Pair<String, JSONObject?>>()
        private val script = mutableMapOf<String, Frame>()

        fun answer(type: String, frame: Frame): Recorder {
            script[type] = frame
            return this
        }

        fun payloadOf(type: String): JSONObject? = sent.first { it.first == type }.second

        fun countOf(type: String): Int = sent.count { it.first == type }

        suspend fun handle(type: String, payload: JSONObject?): Frame {
            sent.add(type to payload)
            return script[type] ?: Frame(type = FrameType.Error)
        }
    }

    private fun recordingStore(r: Recorder) =
        MessengerStore(requestSender = { type, payload -> r.handle(type, payload) })
            
            .also { it._connection.value = true }

    private fun unlinkedFrame(sent: Boolean, receipted: Boolean) = Frame(
        type = FrameType.DeviceUnlinked,
        payload = JSONObject()
            .put("unlinked", true)
            .put("sent", sent)
            .put("receipted", receipted),
    )

    private val noDevices = Frame(
        type = FrameType.DeviceLinksListed,
        payload = JSONObject().put("devices", JSONArray()),
    )

    private val noGuard = Frame(
        type = FrameType.DestroyPinListed,
        payload = JSONObject().put("set", false).put("siblings", JSONArray()),
    )

    @OptIn(ExperimentalCoroutinesApi::class)
    @Test
    fun unlinkCarriesNoPin() = runTest {
        val r = Recorder()
            .answer(FrameType.UnlinkDevice, unlinkedFrame(sent = false, receipted = false))
            .answer(FrameType.DeviceLinksList, noDevices)
            .answer(FrameType.GetDestroyPin, noGuard)

        val out = recordingStore(r).unlinkLinkedDevice("peer-1")

        assertTrue(out.ok)
        val payload = r.payloadOf(FrameType.UnlinkDevice)!!
        assertEquals("peer-1", payload.getString("peer_id"))
        assertFalse(
            "a PIN turns unlink into destroy, and unlink takes no PIN",
            payload.has("pin"),
        )
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    @Test
    fun destroyCarriesTheOtherDevicesPin() = runTest {
        val r = Recorder()
            .answer(FrameType.UnlinkDevice, unlinkedFrame(sent = true, receipted = true))
            .answer(FrameType.DeviceLinksList, noDevices)
            .answer(FrameType.GetDestroyPin, noGuard)

        val out = recordingStore(r).destroyLinkedDevice("peer-1", "4321")

        assertTrue(out.ok)
        assertEquals("4321", r.payloadOf(FrameType.UnlinkDevice)!!.getString("pin"))
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    @Test
    fun aDestroyWithAPinThatIsNotFourDigitsIsNeverSent() = runTest {
        val r = Recorder().answer(FrameType.UnlinkDevice, unlinkedFrame(sent = true, receipted = true))

        val out = recordingStore(r).destroyLinkedDevice("peer-1", "12")

        assertFalse(out.ok)
        assertEquals(0, r.countOf(FrameType.UnlinkDevice))
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    @Test
    fun anUnlinkThatTookRereadsWhatTheDaemonHolds() = runTest {
        val r = Recorder()
            .answer(FrameType.UnlinkDevice, unlinkedFrame(sent = false, receipted = false))
            .answer(FrameType.DeviceLinksList, noDevices)
            .answer(FrameType.GetDestroyPin, noGuard)
        val s = recordingStore(r)
        s._linkedDevices.value = listOf(device())

        s.unlinkLinkedDevice("peer-1")

        assertEquals(1, r.countOf(FrameType.DeviceLinksList))
        assertTrue(
            "without the re-read the screen keeps drawing a device the daemon no longer holds",
            s._linkedDevices.value.isEmpty(),
        )
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    @Test
    fun anUnlinkThatDidNotTakeChangesNothingOnScreen() = runTest {
        val r = Recorder().answer(FrameType.UnlinkDevice, Frame(type = FrameType.Error))
        val s = recordingStore(r)
        s._linkedDevices.value = listOf(device())

        val out = s.unlinkLinkedDevice("peer-1")

        assertFalse(out.ok)
        assertEquals(0, r.countOf(FrameType.DeviceLinksList))
        assertEquals(1, s._linkedDevices.value.size)
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    @Test
    fun theCheckSendsThePeerAndThePinAndChangesNothing() = runTest {
        val r = Recorder().answer(
            FrameType.VerifyDestroyPin,
            Frame(
                type = FrameType.DestroyPinVerified,
                payload = JSONObject().put("answered", true).put("verified", true),
            ),
        )

        val out = recordingStore(r).verifyOtherDestroyPin("peer-1", " 4321 ")

        assertTrue(out.ok)
        assertEquals(VERIFY_ACCEPTED, out.text)
        val payload = r.payloadOf(FrameType.VerifyDestroyPin)!!
        assertEquals("peer-1", payload.getString("peer_id"))
        assertEquals("a pasted PIN arrives with whitespace", "4321", payload.getString("pin"))
        assertEquals("a check destroys nothing", 0, r.countOf(FrameType.UnlinkDevice))
    }

    
    @Test
    fun guardOnlyShowsNoWordsAtAll() {
        assertEquals(WordsSlot.None, wordsSlot(DeviceLinkMode.GuardOnly))
    }

    @Test
    fun theOtherTwoModesEachKeepTheirOwnWordsRow() {
        assertEquals(WordsSlot.Readout, wordsSlot(DeviceLinkMode.Inviting))
        assertEquals(WordsSlot.Field, wordsSlot(DeviceLinkMode.Joining))
    }

    @Test
    fun guardOnlyNamesItselfAndNeverPromisesALink() {
        assertEquals(ACTION_SET_DESTROY_PIN, runTitle(DeviceLinkMode.GuardOnly))
        assertEquals(ACTION_SET_DESTROY_PIN, runAction(DeviceLinkMode.GuardOnly))
    }

    @Test
    fun everyModeGetsItsOwnTitleAndAction() {
        val titles = DeviceLinkMode.entries.map { runTitle(it) }
        val actions = DeviceLinkMode.entries.map { runAction(it) }
        assertEquals("two modes share a title, so one of them is mislabelled",
            titles.size, titles.toSet().size)
        assertEquals("two modes share an action label, so one of them is mislabelled",
            actions.size, actions.toSet().size)
    }

    @Test
    fun theSetAPinHelpSaysWhyTheButtonIsThere() {
        assertTrue(
            "the help must say the devices are linked, or it reads as a stray setting",
            SET_DESTROY_PIN_HELP.contains("linked"),
        )
        assertTrue(
            "the help must say linking did not finish",
            SET_DESTROY_PIN_HELP.contains("did not finish"),
        )
    }

    
    private fun waiting(deadline: Long = 1_000L) =
        DestroyWaiting(peerId = "peer-1", deadline = deadline, startedAt = 0L)

    @Test
    fun aPendingDestroyWinsOverEverythingElse() {
        assertEquals(
            LinkedDevicesView.Waiting,
            linkedDevicesView(run = null, devices = emptyList(), waiting = waiting()),
        )
    }

    
    @Test
    fun aPendingDestroyEvenWinsOverARunOnScreen() {
        val run = DeviceLinkRun(id = 1L, mode = DeviceLinkMode.Inviting)
        assertEquals(
            LinkedDevicesView.Waiting,
            linkedDevicesView(run = run, devices = emptyList(), waiting = waiting()),
        )
    }

    @Test
    fun withNothingPendingTheOtherViewsAreUnchanged() {
        val run = DeviceLinkRun(id = 1L, mode = DeviceLinkMode.Inviting)
        assertEquals(LinkedDevicesView.Run, linkedDevicesView(run, emptyList(), null))
        assertEquals(LinkedDevicesView.Form, linkedDevicesView(null, emptyList(), null))
    }

    
    @Test
    fun theCountdownRoundsUpAndNeverReadsZeroWhileItRuns() {
        assertEquals("00:01", destroyWaitRemaining(deadline = 40L, nowSeconds = 0L))
        assertEquals("00:01", destroyWaitRemaining(deadline = 1L, nowSeconds = 0L))
        assertEquals("06:00", destroyWaitRemaining(deadline = 6 * 3600L, nowSeconds = 0L))
        assertEquals("01:30", destroyWaitRemaining(deadline = 5400L, nowSeconds = 0L))
    }

    @Test
    fun pastTheDeadlineTheCountdownReadsZero() {
        assertEquals("00:00", destroyWaitRemaining(deadline = 10L, nowSeconds = 10L))
        assertEquals("00:00", destroyWaitRemaining(deadline = 10L, nowSeconds = 99L))
    }

    
    @Test
    fun theWaitTextClaimsNoErasureAndSaysStoppingUndoesNothing() {
        val text = destroyWaitText("02:30")
        assertTrue("the time left is not shown", text.contains("02:30"))
        assertTrue(
            "the text must say the outcome stays unknown",
            text.contains("unknown"),
        )
        assertTrue(
            "the text must say stopping does not undo the destroy",
            text.contains("does not undo"),
        )
    }
}
