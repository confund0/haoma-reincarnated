package io.haoma.calculator.core

import io.haoma.disguise.AppState
import io.haoma.disguise.AppStateRepository
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class IdleLockDispatcherTest {
    
    private class Effects {
        val stops = mutableListOf<String>()
        val stopHaoma = mutableListOf<String>()
        val willLock = mutableListOf<String>()
    }

    private fun fixture(
        initialState: AppState = AppState.Warm,
        policy: IdlePolicy? = IdlePolicy(IdlePolicy.Safe, 60),
    ): Triple<IdleLockDispatcher, AppStateRepository, Effects> {
        val state = AppStateRepository(initialState)
        val effects = Effects()
        val dispatcher = IdleLockDispatcher(
            state = state,
            policySource = { policy },
            stopFgs = { effects.stops.add("stop") },
            stopHaomaOnly = { effects.stopHaoma.add("stop-haoma") },
            onWillLock = { action -> effects.willLock.add(action) },
        )
        return Triple(dispatcher, state, effects)
    }

    @Test
    fun softLockTransitionsToLockedSoft() {
        val (d, state, effects) = fixture(policy = IdlePolicy(IdlePolicy.Soft, 60))
        assertTrue(d.fire("test"))
        assertEquals(AppState.Locked.Soft, state.state.value)
        assertTrue("stopFgs must NOT run on soft", effects.stops.isEmpty())
        assertTrue("stopHaomaOnly must NOT run on soft", effects.stopHaoma.isEmpty())
        assertEquals("onWillLock fires once per transition", listOf(IdlePolicy.Soft), effects.willLock)
    }

    @Test
    fun safeLockTransitionsToLockedSafeAndStopsHaomaOnly() {
        val (d, state, effects) = fixture(policy = IdlePolicy(IdlePolicy.Safe, 60))
        assertTrue(d.fire("test"))
        assertEquals(AppState.Locked.Safe, state.state.value)
        assertTrue("stopFgs must NOT run on safe", effects.stops.isEmpty())
        assertEquals("stopHaomaOnly fires exactly once", listOf("stop-haoma"), effects.stopHaoma)
        assertEquals(listOf(IdlePolicy.Safe), effects.willLock)
    }

    @Test
    fun hardLockTransitionsAndStopsFgs() {
        val (d, state, effects) = fixture(policy = IdlePolicy(IdlePolicy.Hard, 60))
        assertTrue(d.fire("test"))
        assertEquals(AppState.Locked.Hard, state.state.value)
        assertEquals(listOf("stop"), effects.stops)
        
        
        assertTrue("stopHaomaOnly must NOT run on hard", effects.stopHaoma.isEmpty())
        assertEquals(listOf(IdlePolicy.Hard), effects.willLock)
    }

    @Test
    fun unknownActionFallsThroughToSafe() {
        val (d, state, effects) = fixture(policy = IdlePolicy("nonsense", 60))
        assertTrue(d.fire("test"))
        
        
        assertEquals(AppState.Locked.Safe, state.state.value)
        assertEquals(listOf("stop-haoma"), effects.stopHaoma)
        assertTrue("stopFgs must NOT run on unknown", effects.stops.isEmpty())
        assertEquals(listOf("nonsense"), effects.willLock)
    }

    @Test
    fun nullPolicyShortCircuits() {
        val (d, state, effects) = fixture(policy = null)
        assertFalse(d.fire("cold-boot"))
        assertEquals(AppState.Warm, state.state.value)
        assertTrue(effects.stops.isEmpty())
        assertTrue(effects.stopHaoma.isEmpty())
        assertTrue("onWillLock must NOT fire when no policy is installed", effects.willLock.isEmpty())
    }

    @Test
    fun alreadyLockedIsIdempotent() {
        val (d, state, effects) = fixture(
            initialState = AppState.Locked.Soft,
            policy = IdlePolicy(IdlePolicy.Hard, 60),
        )
        assertFalse("already-locked must not fire again", d.fire("os-lock"))
        assertEquals("state must NOT be promoted soft→hard", AppState.Locked.Soft, state.state.value)
        assertTrue(effects.stops.isEmpty())
        assertTrue(effects.stopHaoma.isEmpty())
        assertTrue("onWillLock must NOT fire when already-locked", effects.willLock.isEmpty())
    }

    @Test
    fun alreadySafeLockedIsIdempotent() {
        
        
        val (d, state, effects) = fixture(
            initialState = AppState.Locked.Safe,
            policy = IdlePolicy(IdlePolicy.Safe, 60),
        )
        assertFalse(d.fire("screen-off"))
        assertEquals(AppState.Locked.Safe, state.state.value)
        assertTrue(effects.stopHaoma.isEmpty())
        assertTrue(effects.willLock.isEmpty())
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

    @Test
    fun onWillLockExceptionDoesNotAbortTransition() {
        
        
        val state = AppStateRepository(AppState.Warm)
        val d = IdleLockDispatcher(
            state = state,
            policySource = { IdlePolicy(IdlePolicy.Hard, 60) },
            stopFgs = {  },
            onWillLock = { throw IllegalStateException("synthetic test failure") },
        )
        assertTrue(d.fire("test"))
        assertEquals(AppState.Locked.Hard, state.state.value)
    }
}
