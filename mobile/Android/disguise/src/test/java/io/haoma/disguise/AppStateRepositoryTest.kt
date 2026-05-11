package io.haoma.disguise

import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Test

class AppStateRepositoryTest {
    @Test
    fun `default initial state is hard locked`() {
        val repo = AppStateRepository()
        assertSame(AppState.Locked.Hard, repo.state.value)
    }

    @Test
    fun `caller can override the initial state`() {
        val repo = AppStateRepository(initial = AppState.Warm)
        assertSame(AppState.Warm, repo.state.value)
    }

    @Test
    fun `update flips the StateFlow value`() {
        val repo = AppStateRepository()
        repo.update(AppState.Warm)
        assertSame(AppState.Warm, repo.state.value)

        repo.update(AppState.Locked.Soft)
        assertSame(AppState.Locked.Soft, repo.state.value)

        repo.update(AppState.Locked.Safe)
        assertSame(AppState.Locked.Safe, repo.state.value)
    }

    @Test
    fun `lock variants are distinct singletons`() {
        assertEquals(AppState.Locked.Soft, AppState.Locked.Soft)
        assertSame(AppState.Locked.Soft, AppState.Locked.Soft)
        assert(AppState.Locked.Soft != AppState.Locked.Safe)
        assert(AppState.Locked.Safe != AppState.Locked.Hard)
    }
}

class LoggingRevealControllerTest {
    @Test
    fun `arm submit cancel each fire one log line`() {
        val lines = mutableListOf<String>()
        val rc = LoggingRevealController { lines += it }

        rc.arm()
        rc.submit("12345")
        rc.cancel()

        assertEquals(listOf("arm", "submit token=12345", "cancel"), lines)
    }
}
