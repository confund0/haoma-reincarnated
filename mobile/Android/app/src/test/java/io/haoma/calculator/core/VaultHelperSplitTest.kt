package io.haoma.calculator.core

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Test

class VaultHelperSplitTest {
    @Test
    fun splitsSecretsAndDerivesPolicyFromPayload() {
        val secrets = """{"haomad_token":"x"}"""
        val payload = """{"haomad_token":"x","idle_action":"hard-lock","idle_timeout_seconds":42}"""
        val combined = "$secrets\n$payload\n".toByteArray(Charsets.UTF_8)

        val out = VaultHelper.splitOutput(combined)

        assertArrayEquals(secrets.toByteArray(Charsets.UTF_8), out.secrets)
        assertArrayEquals(payload.toByteArray(Charsets.UTF_8), out.payload)
        assertEquals(IdlePolicy.Hard, out.policy.action)
        assertEquals(42, out.policy.timeoutSeconds)
    }

    @Test
    fun missingPayloadFallsBackToDefaults() {
        
        val secrets = """{"haomad_token":"x"}"""
        val out = VaultHelper.splitOutput(secrets.toByteArray(Charsets.UTF_8))
        assertArrayEquals(secrets.toByteArray(Charsets.UTF_8), out.secrets)
        assertEquals(IdlePolicy.Default, out.policy)
        assertEquals(0, out.payload.size)
    }

    @Test
    fun emptyPayloadFallsBackToDefaults() {
        
        val secrets = """{"haomad_token":"x"}"""
        val combined = "$secrets\n".toByteArray(Charsets.UTF_8)
        val out = VaultHelper.splitOutput(combined)
        assertArrayEquals(secrets.toByteArray(Charsets.UTF_8), out.secrets)
        assertEquals(IdlePolicy.Default, out.policy)
        assertEquals(0, out.payload.size)
    }

    @Test
    fun unknownActionInPayloadDefaultsToSafeLock() {
        val secrets = """{"haomad_token":"x"}"""
        val payload = """{"idle_action":"explode","idle_timeout_seconds":60}"""
        val combined = "$secrets\n$payload\n".toByteArray(Charsets.UTF_8)
        val out = VaultHelper.splitOutput(combined)
        assertEquals(IdlePolicy.Safe, out.policy.action)
        assertEquals(60, out.policy.timeoutSeconds)
    }

    @Test
    fun nonPositiveTimeoutFallsBackToDefault() {
        val secrets = """{"haomad_token":"x"}"""
        val payload = """{"idle_action":"soft-lock","idle_timeout_seconds":0}"""
        val combined = "$secrets\n$payload\n".toByteArray(Charsets.UTF_8)
        val out = VaultHelper.splitOutput(combined)
        assertEquals(IdlePolicy.Soft, out.policy.action)
        assertEquals(IdlePolicy.DefaultTimeoutSeconds, out.policy.timeoutSeconds)
    }

    @Test
    fun garbledPayloadStillReturnsBytes() {
        
        
        val secrets = """{"haomad_token":"x"}"""
        val payload = "not json"
        val combined = "$secrets\n$payload\n".toByteArray(Charsets.UTF_8)
        val out = VaultHelper.splitOutput(combined)
        assertArrayEquals(payload.toByteArray(Charsets.UTF_8), out.payload)
        assertEquals(IdlePolicy.Default, out.policy)
    }
}
