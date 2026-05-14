package io.haoma.calculator.messenger.calls

import android.content.Context
import android.os.PowerManager
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.CallEntry
import io.haoma.calculator.messenger.CallStatus
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.launchIn
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.onEach


class ProximityController(
    app: Context,
    private val activeCallsSource: StateFlow<Map<String, CallEntry>>,
) {
    private val scope = CoroutineScope(
        SupervisorJob() + Dispatchers.Main + Logger.coroutineExceptionHandler,
    )
    private val powerManager = app.getSystemService(Context.POWER_SERVICE) as PowerManager
    private val supported: Boolean =
        powerManager.isWakeLockLevelSupported(PowerManager.PROXIMITY_SCREEN_OFF_WAKE_LOCK)
    private val wakeLock: PowerManager.WakeLock? =
        if (supported) {
            powerManager.newWakeLock(
                PowerManager.PROXIMITY_SCREEN_OFF_WAKE_LOCK,
                "haoma:proximity",
            ).apply { setReferenceCounted(false) }
        } else null

    fun start() {
        if (!supported) {
            Logger.i("proximity", "PROXIMITY_SCREEN_OFF_WAKE_LOCK unsupported on this device — no-op")
            return
        }
        activeCallsSource
            .map { calls -> calls.values.any { it.status == CallStatus.Accepted } }
            .distinctUntilChanged()
            .onEach { active -> if (active) acquire() else release() }
            .launchIn(scope)
    }

    
    fun releaseLockIfHeld() {
        release()
    }

    private fun acquire() {
        val lock = wakeLock ?: return
        if (lock.isHeld) return
        try {
            lock.acquire()
            Logger.i("proximity", "wake lock acquired (call active)")
        } catch (t: Throwable) {
            Logger.w("proximity", "acquire failed: ${t.message}")
        }
    }

    private fun release() {
        val lock = wakeLock ?: return
        if (!lock.isHeld) return
        try {
            lock.release()
            Logger.i("proximity", "wake lock released (call inactive)")
        } catch (t: Throwable) {
            Logger.w("proximity", "release failed: ${t.message}")
        }
    }
}
