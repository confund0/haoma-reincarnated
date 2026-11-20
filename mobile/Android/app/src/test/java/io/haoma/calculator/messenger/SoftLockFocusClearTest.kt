package io.haoma.calculator.messenger

import org.junit.Assert.assertEquals
import org.junit.Test


class SoftLockFocusClearTest {

    @Test
    fun `soft-lock clears the remembered focus chat`() {
        val store = MessengerStore()
        store.currentFocusChatId = "chat-1"

        store.syncLockState(true)

        assertEquals("", store.currentFocusChatId)
    }

    @Test
    fun `unlock does not touch the remembered focus chat`() {
        val store = MessengerStore()
        store.currentFocusChatId = "chat-1"
        store.syncLockState(true) 
        store.currentFocusChatId = "chat-1" 

        store.syncLockState(false)

        assertEquals("chat-1", store.currentFocusChatId)
    }

    @Test
    fun `repeating the same lock state is a no-op, focus stays`() {
        
        
        val store = MessengerStore()
        store.currentFocusChatId = "chat-1"

        store.syncLockState(false)

        assertEquals("chat-1", store.currentFocusChatId)
    }
}
