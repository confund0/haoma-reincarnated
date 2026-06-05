package io.haoma.calculator.messenger

import kotlinx.coroutines.flow.update
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test


class ChatSearchStateTest {

    private fun match(msgId: String, displayTs: Long): ChatSearchMatch =
        ChatSearchMatch(msgId = msgId, displayTs = displayTs, bodyOffset = 0)

    private fun seed(store: MessengerStore, chatId: String, msgIds: List<String>) {
        
        
        store.openChatSearch(chatId)
        val matches = msgIds.mapIndexed { i, id -> match(id, 1000L - i * 10L) }
        val cur = store.chatSearch.value!!
        store._chatSearch.value = cur.copy(
            query = "q",
            matches = matches,
            cursorIdx = 0,
            truncated = false,
            loading = false,
        )
    }

    @Test
    fun openChatSearchSetsEmptyStateForChat() {
        val store = MessengerStore()
        store.openChatSearch("chat-1")
        val s = store.chatSearch.value
        assertNotNull(s)
        assertEquals("chat-1", s!!.chatId)
        assertEquals("", s.query)
        assertTrue(s.matches.isEmpty())
        assertEquals(0, s.cursorIdx)
    }

    @Test
    fun openChatSearchRejectsEmptyChatId() {
        val store = MessengerStore()
        store.openChatSearch("")
        assertNull("empty chatId must not open a search", store.chatSearch.value)
    }

    @Test
    fun setChatSearchQueryUpdatesBufferWithoutFiring() {
        val store = MessengerStore()
        store.openChatSearch("chat-1")
        store.setChatSearchQuery("foo")
        assertEquals("foo", store.chatSearch.value!!.query)
        assertTrue("no matches yet — IPC fires only on submit", store.chatSearch.value!!.matches.isEmpty())
    }

    @Test
    fun setChatSearchQueryNoOpWhenBarClosed() {
        val store = MessengerStore()
        store.setChatSearchQuery("foo")
        assertNull("query update on a closed bar must not implicitly open it", store.chatSearch.value)
    }

    @Test
    fun closeChatSearchClearsState() {
        val store = MessengerStore()
        store.openChatSearch("chat-1")
        store.closeChatSearch()
        assertNull(store.chatSearch.value)
    }

    @Test
    fun closeChatSearchDropsPendingAnchorForChat() {
        
        
        val store = MessengerStore()
        seed(store, "chat-1", listOf("msg-a", "msg-b"))
        store.bumpAnchor("chat-1", "msg-a")
        assertNotNull(store.pendingAnchors.value["chat-1"])
        store.closeChatSearch()
        assertNull("X tap drops the pending anchor", store.pendingAnchors.value["chat-1"])
    }

    @Test
    fun stepChatSearchWrapsPastEndToStart() {
        
        val store = MessengerStore()
        seed(store, "chat-1", listOf("msg-a", "msg-b", "msg-c"))
        store._chatSearch.update { it!!.copy(cursorIdx = 2) }
        store.stepChatSearch(+1)
        assertEquals(0, store.chatSearch.value!!.cursorIdx)
    }

    @Test
    fun stepChatSearchWrapsBeforeStartToEnd() {
        
        val store = MessengerStore()
        seed(store, "chat-1", listOf("msg-a", "msg-b", "msg-c"))
        store.stepChatSearch(-1)
        assertEquals(2, store.chatSearch.value!!.cursorIdx)
    }

    @Test
    fun stepChatSearchPolarityIsOlderEqualsPlusOne() {
        
        
        val store = MessengerStore()
        seed(store, "chat-1", listOf("newest", "middle", "oldest"))
        store.stepChatSearch(+1)
        assertEquals(1, store.chatSearch.value!!.cursorIdx)
        store.stepChatSearch(+1)
        assertEquals(2, store.chatSearch.value!!.cursorIdx)
    }

    @Test
    fun stepChatSearchEmptyMatchesNoOp() {
        val store = MessengerStore()
        store.openChatSearch("chat-1")
        store.stepChatSearch(+1)
        assertEquals("no matches: cursor stays at 0", 0, store.chatSearch.value!!.cursorIdx)
    }

    @Test
    fun stepChatSearchClosedBarNoOp() {
        val store = MessengerStore()
        store.stepChatSearch(+1) 
        assertNull(store.chatSearch.value)
    }

    @Test
    fun stepChatSearchPublishesFreshAnchor() {
        
        
        val store = MessengerStore()
        seed(store, "chat-1", listOf("msg-a", "msg-b"))
        store.stepChatSearch(+1)
        val anchor = store.pendingAnchors.value["chat-1"]
        assertNotNull(anchor)
        assertEquals("msg-b", anchor!!.msgId)
    }
}
