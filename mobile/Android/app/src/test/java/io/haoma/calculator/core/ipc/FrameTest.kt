package io.haoma.calculator.core.ipc

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Test

class FrameTest {
    @Test
    fun helloRoundTrip() {
        val payload = JSONObject().apply {
            put("client_name", "haoma-android")
            put("client_version", "0.0.1")
        }
        val original = Frame(type = FrameType.Hello, id = "handshake", payload = payload)
        val decoded = Frame.decode(original.encode())
        assertEquals("hello", decoded.type)
        assertEquals("handshake", decoded.id)
        assertNotNull(decoded.payload)
        assertEquals("haoma-android", decoded.payload!!.getString("client_name"))
        assertEquals("0.0.1", decoded.payload!!.getString("client_version"))
    }

    @Test
    fun pushFrameOmitsId() {
        val payload = JSONObject().apply { put("daemon_version", "0.42.0") }
        val original = Frame(type = FrameType.Welcome, id = null, payload = payload)
        val decoded = Frame.decode(original.encode())
        assertEquals("system.welcome", decoded.type)
        assertNull(decoded.id)
        assertNotNull(decoded.payload)
    }

    @Test
    fun emptyPayloadDecodesToNull() {
        
        val text = """{"type":"ping","id":"k-7"}"""
        val decoded = Frame.decode(text)
        assertEquals("ping", decoded.type)
        assertEquals("k-7", decoded.id)
        assertNull(decoded.payload)
    }

    @Test
    fun missingTypeRejected() {
        val text = """{"id":"x"}"""
        assertThrows(IllegalArgumentException::class.java) { Frame.decode(text) }
    }

    @Test
    fun emptyIdNormalisedToNull() {
        
        
        val text = """{"type":"system.error","id":"","payload":{"reason":"x"}}"""
        val decoded = Frame.decode(text)
        assertNull(decoded.id)
    }
}
