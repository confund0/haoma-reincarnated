package io.haoma.calculator.core

import io.haoma.calculator.log.Logger
import io.haoma.disguise.AppState
import io.haoma.disguise.AppStateRepository


class IdleLockDispatcher(
    private val state: AppStateRepository,
    private val policySource: () -> IdlePolicy?,
    private val stopFgs: () -> Unit,
) {
    
    fun fire(reason: String): Boolean {
        val current = state.state.value
        if (current is AppState.Locked) {
            Logger.i("idle", "fire reason=$reason skipped (already locked: ${current::class.simpleName})")
            return false
        }
        val policy = policySource()
        if (policy == null) {
            Logger.i("idle", "fire reason=$reason skipped (no policy — vault never opened this process)")
            return false
        }
        return when (policy.action) {
            IdlePolicy.Soft -> {
                Logger.i("idle", "fire reason=$reason → soft-lock")
                state.update(AppState.Locked.Soft)
                true
            }
            IdlePolicy.Safe -> {
                
                
                Logger.i(
                    "idle",
                    "fire reason=$reason → safe-lock collapsed to soft (partial-teardown wiring TODO)",
                )
                state.update(AppState.Locked.Soft)
                true
            }
            IdlePolicy.Hard -> {
                Logger.i("idle", "fire reason=$reason → hard-lock (stopping FGS)")
                state.update(AppState.Locked.Hard)
                stopFgs()
                true
            }
            else -> {
                
                Logger.w("idle", "fire reason=$reason unknown action=${policy.action}; treating as safe-lock (collapsed to soft pre-M-7)")
                state.update(AppState.Locked.Soft)
                true
            }
        }
    }
}
