package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.Frame
import io.haoma.calculator.core.ipc.FrameType
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference
import kotlinx.coroutines.runBlocking
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test


class WipeDeviceTest {

    
    @Test
    fun `the word is checked first, so a wrong word leaves the unlock answer unread`() {
        assertEquals(
            WipeGuardStep.WrongWord,
            wipeGuardStep(word = "erase", unlockAnswer = "78963", bypassKey = ""),
        )
        assertEquals(
            WipeGuardStep.WrongWord,
            wipeGuardStep(word = "", unlockAnswer = "78963", bypassKey = ""),
        )
        assertEquals(
            WipeGuardStep.WrongWord,
            wipeGuardStep(word = "ERASE THIS", unlockAnswer = "78963", bypassKey = ""),
        )
    }

    
    @Test
    fun `a wrong word is refused even when the unlock answer is the bypass key`() {
        assertEquals(
            WipeGuardStep.WrongWord,
            wipeGuardStep(word = "", unlockAnswer = "7", bypassKey = "7"),
        )
        assertEquals(
            WipeGuardStep.WrongWord,
            wipeGuardStep(word = "erase", unlockAnswer = "7", bypassKey = "7"),
        )
    }

    @Test
    fun `the word survives a keyboard that adds a space`() {
        assertEquals(
            WipeGuardStep.CheckUnlockSecret,
            wipeGuardStep(word = " ERASE ", unlockAnswer = "78963", bypassKey = ""),
        )
    }

    @Test
    fun `an unlock answer that is not the bypass key goes to the sidecar`() {
        assertEquals(
            WipeGuardStep.CheckUnlockSecret,
            wipeGuardStep(word = "ERASE", unlockAnswer = "78963", bypassKey = "7"),
        )
    }

    @Test
    fun `the bypass key is accepted where the slot is on`() {
        assertEquals(
            WipeGuardStep.BypassKeyGiven,
            wipeGuardStep(word = "ERASE", unlockAnswer = "7", bypassKey = "7"),
        )
        assertEquals(
            WipeGuardStep.BypassKeyGiven,
            wipeGuardStep(word = "ERASE", unlockAnswer = " 7 ", bypassKey = "7"),
        )
    }

    
    @Test
    fun `an empty answer never matches an empty bypass slot`() {
        assertEquals(
            WipeGuardStep.CheckUnlockSecret,
            wipeGuardStep(word = "ERASE", unlockAnswer = "", bypassKey = ""),
        )
        assertEquals(
            WipeGuardStep.CheckUnlockSecret,
            wipeGuardStep(word = "ERASE", unlockAnswer = "   ", bypassKey = ""),
        )
    }

    
    private class Sent {
        val count = AtomicInteger(0)
        val lastType = AtomicReference<String?>(null)
    }

    private fun storeWithNoSidecar(sent: Sent) = MessengerStore(
        requestSender = { type, _ ->
            sent.count.incrementAndGet()
            sent.lastType.set(type)
            Frame(type = FrameType.DeviceWiping, payload = JSONObject().put("started", true))
        },
    )

    @Test
    fun `a wrong word is refused for the word, not for the missing sidecar`() = runBlocking {
        val sent = Sent()
        val result = storeWithNoSidecar(sent).wipeThisDevice(word = "nope", unlockAnswer = "78963")
        assertFalse(result.ok)
        assertEquals(WIPE_WRONG_WORD, result.text)
        assertEquals(0, sent.count.get())
    }

    @Test
    fun `an unlock answer that cannot be checked erases nothing`() = runBlocking {
        val sent = Sent()
        val result = storeWithNoSidecar(sent).wipeThisDevice(word = "ERASE", unlockAnswer = "78963")
        assertFalse(result.ok)
        assertEquals(WIPE_NO_UNLOCK_CHECK, result.text)
        assertEquals(0, sent.count.get())
        assertNull(sent.lastType.get())
    }

    
    private fun storeAnswering(reply: (String) -> Frame) = MessengerStore(
        requestSender = { type, _ -> reply(type) },
    )

    @Test
    fun `a started erasure is reported as started, and the verb is the wire's`() = runBlocking {
        val asked = AtomicReference<String?>(null)
        val store = storeAnswering { type ->
            asked.set(type)
            Frame(type = FrameType.DeviceWiping, payload = JSONObject().put("started", true))
        }
        val result = store.sendWipeRequest()
        assertTrue(result.ok)
        assertEquals(WIPE_STARTED, result.text)
        assertEquals("wipe_device", asked.get())
    }

    @Test
    fun `a daemon that answers not started leaves the device unchanged`() = runBlocking {
        val store = storeAnswering {
            Frame(type = FrameType.DeviceWiping, payload = JSONObject().put("started", false))
        }
        val result = store.sendWipeRequest()
        assertFalse(result.ok)
        assertEquals(WIPE_REFUSED, result.text)
    }

    @Test
    fun `a refusal leaves the device unchanged`() = runBlocking {
        val store = storeAnswering {
            Frame(type = FrameType.Error, payload = JSONObject().put("code", "not_ready"))
        }
        val result = store.sendWipeRequest()
        assertFalse(result.ok)
        assertEquals(WIPE_REFUSED, result.text)
    }

    
    @Test
    fun `an answer that never arrives does not say the device is unchanged`() = runBlocking {
        val store = MessengerStore(
            requestSender = { _, _ -> throw IllegalStateException("there is no daemon to ask") },
        )
        val result = store.sendWipeRequest()
        assertFalse(result.ok)
        assertEquals(WIPE_NO_ANSWER, result.text)
        assertFalse(result.text.contains("unchanged"))
    }

    
    @Test
    fun `a device_wiping with no body is not a started erasure`() = runBlocking {
        val store = storeAnswering { Frame(type = FrameType.DeviceWiping) }
        val result = store.sendWipeRequest()
        assertFalse(result.ok)
        assertEquals(WIPE_NO_ANSWER, result.text)
        assertFalse(result.text.contains("unchanged"))
    }

    
    @Test
    fun `an answer of the wrong type does not say the device is unchanged`() = runBlocking {
        val store = storeAnswering { Frame(type = FrameType.Welcome) }
        val result = store.sendWipeRequest()
        assertFalse(result.ok)
        assertEquals(WIPE_NO_ANSWER, result.text)
    }

    
    @Test
    fun `the bypass hint says the key is accepted and never says what it is`() {
        assertTrue(WIPE_HINT_BYPASS.contains("bypass key"))
        assertTrue(WIPE_HINT_BYPASS.contains("accepted"))
        for (key in listOf("7", "0", "42", "%", "+", "=")) {
            assertFalse("[$key]", WIPE_HINT_BYPASS.contains("[$key]"))
        }
    }

    
    @Test
    fun `the body says the erase cannot be undone and that the app closes`() {
        assertTrue(WIPE_BODY_NO_UNDO.contains("cannot be undone"))
        assertTrue(WIPE_BODY_STAYS.contains("This app closes when the erase starts."))
        assertTrue(WIPE_BODY_SAVED_FILES.contains("saved manually"))
    }

    
    @Test
    fun `no sentence on this screen says wipe`() {
        val everySentence = listOf(
            WIPE_BLOCK_LABEL, WIPE_BODY_GOES, WIPE_BODY_NO_UNDO, WIPE_BODY_STAYS,
            WIPE_BODY_SAVED_FILES, WIPE_FIELD_UNLOCK, WIPE_FIELD_WORD,
            WIPE_HINT_UNLOCK, WIPE_HINT_BYPASS, WIPE_ACTION, WIPE_WRONG_WORD,
            WIPE_WRONG_PIN, WIPE_NO_UNLOCK_CHECK, WIPE_ASKING, WIPE_REFUSED,
            WIPE_NO_ANSWER, WIPE_STARTED,
        ) + WIPE_BODY_ITEMS
        for (sentence in everySentence) {
            assertFalse(sentence, sentence.lowercase().contains("wipe"))
        }
    }
}
