package io.haoma.calculator.core

import io.haoma.disguise.AppState
import io.haoma.disguise.AppStateRepository
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class IdleLockDispatcherTest {
    private fun fixture(
        initialState: AppState = AppState.Warm,
        policy: IdlePolicy? = IdlePolicy(IdlePolicy.Safe, 60),
    ): Triple<IdleLockDispatcher, AppStateRepository, MutableList<String>> {
        val state = AppStateRepository(initialState)
        val stops = mutableListOf<String>()
        val dispatcher = IdleLockDispatcher(
            state = state,
            policySource = { policy },
            stopFgs = { stops.add("stop") },
        )
        return Triple(dispatcher, state, stops)
    }

    @Test
    fun softLockTransitionsToLockedSoft() {
        val (d, state, stops) = fixture(policy = IdlePolicy(IdlePolicy.Soft, 60))
        assertTrue(d.fire("test"))
        assertEquals(AppState.Locked.Soft, state.state.value)
        assertTrue("stopFgs must NOT run on soft", stops.isEmpty())
    }

    @Test
    fun safeLockCollapsesToSoftUntilM7() {
        
        
        val (d, state, stops) = fixture(policy = IdlePolicy(IdlePolicy.Safe, 60))
        assertTrue(d.fire("test"))
        assertEquals(AppState.Locked.Soft, state.state.value)
        assertTrue("stopFgs must NOT run on safe", stops.isEmpty())
    }

    @Test
    fun hardLockTransitionsAndStopsFgs() {
        val (d, state, stops) = fixture(policy = IdlePolicy(IdlePolicy.Hard, 60))
        assertTrue(d.fire("test"))
        assertEquals(AppState.Locked.Hard, state.state.value)
        assertEquals(listOf("stop"), stops)
    }

    @Test
    fun unknownActionFallsThroughSafeBranchAndCollapsesToSoft() {
        val (d, state, _) = fixture(policy = IdlePolicy("nonsense", 60))
        assertTrue(d.fire("test"))
        
        
        assertEquals(AppState.Locked.Soft, state.state.value)
    }

    @Test
    fun nullPolicyShortCircuits() {
        val (d, state, stops) = fixture(policy = null)
        assertFalse(d.fire("cold-boot"))
        assertEquals(AppState.Warm, state.state.value)
        assertTrue(stops.isEmpty())
    }

    @Test
    fun alreadyLockedIsIdempotent() {
        val (d, state, stops) = fixture(
            initialState = AppState.Locked.Soft,
            policy = IdlePolicy(IdlePolicy.Hard, 60),
        )
        assertFalse("already-locked must not fire again", d.fire("os-lock"))
        assertEquals("state must NOT be promoted soft→hard", AppState.Locked.Soft, state.state.value)
        assertTrue(stops.isEmpty())
    }

    @Test
    fun passphraseStateIsIdempotent() {
        val (d, state, _) = fixture(
            initialState = AppState.Locked.Passphrase,
            policy = IdlePolicy(IdlePolicy.Hard, 60),
        )
        assertFalse(d.fire("background"))
        assertEquals(AppState.Locked.Passphrase, state.state.value)
    }
}
