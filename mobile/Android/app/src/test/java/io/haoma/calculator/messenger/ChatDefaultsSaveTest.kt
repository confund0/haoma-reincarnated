package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.Frame
import io.haoma.calculator.core.ipc.FrameType
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.currentTime
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.yield
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test


@OptIn(ExperimentalCoroutinesApi::class)
class ChatDefaultsSaveTest {

    
    private fun storeWithDaemon(sent: MutableList<Frame> = mutableListOf()) =
        MessengerStore(frameSender = { sent.add(it); true })

    private fun broadcast(retentionSec: Long, receipts: Boolean) = Frame(
        type = FrameType.ChatDefaultsChanged,
        payload = JSONObject()
            .put("retention_sec", retentionSec)
            .put("send_receipts", receipts),
    )

    @Test
    fun saveReturnsWhenTheBroadcastArrives() = runTest {
        val store = storeWithDaemon()
        val want = ChatDefaultsSettings(retentionSeconds = 3600L, sendReceipts = false)

        var result: Result<Unit>? = null
        val started = currentTime
        val job = launch { result = store.saveChatDefaults(want) }

        
        yield()
        store.dispatch(broadcast(3600L, false))
        job.join()

        val elapsed = currentTime - started
        assertTrue("save failed: ${result?.exceptionOrNull()?.message}", result!!.isSuccess)
        
        
        assertTrue("save took ${elapsed}ms — it waited for a reply that never comes", elapsed < 1_000)
    }

    @Test
    fun saveDoesNotFinishOnSomebodyElsesValues() = runTest {
        val store = storeWithDaemon()
        val want = ChatDefaultsSettings(retentionSeconds = 3600L, sendReceipts = false)

        var result: Result<Unit>? = null
        val job = launch { result = store.saveChatDefaults(want) }

        yield()
        
        
        store.dispatch(broadcast(86_400L, true))
        yield()
        assertEquals("a foreign broadcast ended the save", null, result)

        store.dispatch(broadcast(3600L, false))
        job.join()
        assertTrue(result!!.isSuccess)
    }

    @Test
    fun saveGivesUpWhenNothingComesBack() = runTest {
        val store = storeWithDaemon()
        val result = store.saveChatDefaults(
            ChatDefaultsSettings(retentionSeconds = 3600L, sendReceipts = false),
        )
        assertTrue("a save nobody confirmed must not read as saved", result.isFailure)
        assertEquals(CHAT_DEFAULTS_NOT_SAVED, result.exceptionOrNull()?.message)
    }

    @Test
    fun saveWithoutADaemonSaysSo() = runTest {
        val store = MessengerStore(frameSender = { false })
        val result = store.saveChatDefaults(
            ChatDefaultsSettings(retentionSeconds = 3600L, sendReceipts = false),
        )
        assertTrue(result.isFailure)
        assertEquals(CHAT_DEFAULTS_NOT_CONNECTED, result.exceptionOrNull()?.message)
    }
}
