package io.haoma.calculator.core

import io.haoma.calculator.log.Logger
import io.haoma.disguise.AppState
import io.haoma.disguise.AppStateRepository
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch


class ForegroundIdleTimer(
    private val state: AppStateRepository,
    private val policySource: () -> IdlePolicy?,
    private val dispatcher: IdleLockDispatcher,
    private val nowMillis: () -> Long = System::currentTimeMillis,
) {
    private val scope = CoroutineScope(
        SupervisorJob() + Dispatchers.Default + Logger.coroutineExceptionHandler,
    )

    @Volatile private var lastTouchMillis: Long = nowMillis()
    @Volatile private var job: Job? = null

    fun touch() {
        lastTouchMillis = nowMillis()
    }

    fun resume() {
        touch()
        if (job?.isActive == true) return
        job = scope.launch { runLoop() }
        Logger.i("idle-timer", "resumed")
    }

    fun pause() {
        job?.cancel()
        job = null
        Logger.i("idle-timer", "paused")
    }

    private suspend fun runLoop() {
        while (scope.isActive) {
            delay(TickMillis)
            if (state.state.value !is AppState.Warm) continue
            val policy = policySource() ?: continue
            val timeoutMs = policy.timeoutSeconds * 1000L
            if (timeoutMs <= 0) continue
            val elapsed = nowMillis() - lastTouchMillis
            if (elapsed >= timeoutMs) {
                Logger.i("idle-timer", "timeout reached elapsed=${elapsed}ms timeout=${timeoutMs}ms")
                lastTouchMillis = nowMillis() 
                dispatcher.fire("idle-timer")
            }
        }
    }

    companion object {
        private const val TickMillis = 1_000L
    }
}
