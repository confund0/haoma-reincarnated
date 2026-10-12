package io.haoma.calculator.core

import android.content.Context
import io.haoma.calculator.log.Logger
import io.haoma.disguise.AppState
import io.haoma.disguise.AppStateRepository
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.coroutines.cancellation.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.withTimeoutOrNull


class UnlockManager(
    private val app: Context,
    private val state: AppStateRepository,
    private val policySink: (IdlePolicy) -> Unit = {},
    private val sessionSink: (VaultSession?) -> Unit = {},
    
    
    private val passphraseDefaultSink: (Boolean) -> Unit = {},
    
    
    private val restoredSink: () -> Unit = {},
    
    
    private val restorePendingSink: () -> Unit = {},
    
    
    private val stopDaemonsForRestore: suspend () -> Boolean = { stopServiceForRestore(app) },
) {
    sealed interface Outcome {
        data object Warmed : Outcome

        
        data object NeedsPassphrase : Outcome

        
        data class SpawnFailed(val message: String) : Outcome

        
        data object WrongPassphrase : Outcome

        
        data class RestoreFailed(val message: String) : Outcome
    }

    private val inFlight = AtomicBoolean(false)

    
    fun handleHardSlideMatch() {
        Logger.i("unlock", "slide match → Passphrase (always-prompt UX)")
        state.update(AppState.Locked.Passphrase)
    }

    
    suspend fun submitPassphrase(passphrase: ByteArray): Outcome =
        runUnseal(passphrase)

    
    suspend fun submitDefaultPassphrase(): Outcome =
        runUnseal(VaultHelper.DefaultPassphraseBytes)

    
    suspend fun submitStagedRestoreCommit(
        passphrase: ByteArray,
        stagingPath: String,
    ): Outcome = runCommit(passphrase, stagingPath)

    private suspend fun runUnseal(passphrase: ByteArray): Outcome {
        if (!inFlight.compareAndSet(false, true)) {
            Logger.w("unlock", "unseal attempt dropped: another in flight")
            return Outcome.WrongPassphrase
        }
        try {
            val outcome = withContext(Dispatchers.IO) {
                tryUnsealAndSpawn(passphrase, isDefault = false)
            }
            if (outcome == Outcome.Warmed) {
                state.update(AppState.Warm)
            }
            return outcome
        } finally {
            inFlight.set(false)
        }
    }

    private suspend fun runCommit(passphrase: ByteArray, stagingPath: String): Outcome {
        if (!inFlight.compareAndSet(false, true)) {
            Logger.w("unlock", "commit attempt dropped: another in flight")
            return Outcome.WrongPassphrase
        }
        try {
            val outcome = withContext(Dispatchers.IO) {
                tryCommitAndSpawn(passphrase, stagingPath)
            }
            if (outcome == Outcome.Warmed) {
                state.update(AppState.Warm)
            }
            return outcome
        } finally {
            inFlight.set(false)
        }
    }

    
    fun revertToHard() {
        Logger.i("unlock", "revert → Hard")
        state.update(AppState.Locked.Hard)
    }

    
    private suspend fun tryCommitAndSpawn(passphrase: ByteArray, stagingPath: String): Outcome {
        val attempt = try {
            commitStoppingDaemons(
                commit = { VaultHelper.archiveCommit(app, stagingPath, passphrase) },
                stopDaemons = stopDaemonsForRestore,
            )
        } catch (e: CancellationException) {
            throw e
        } catch (t: Throwable) {
            
            
            Logger.w("unlock", "archive-commit failed: ${t.message}")
            restoredSink()
            return Outcome.RestoreFailed(RESTORE_UNKNOWN)
        }
        val result = when (attempt) {
            RestoreAttempt.StopFailed -> {
                Logger.w("unlock", "restore not applied: the service did not stop")
                return Outcome.RestoreFailed(RESTORE_NOT_APPLIED)
            }
            is RestoreAttempt.Done -> attempt.result
        }
        return when (result) {
            is VaultHelper.CommitResult.Swapped -> {
                restoredSink()
                finishUnseal(passphrase, result.unsealed)
            }
            VaultHelper.CommitResult.SwappedNoBundle -> {
                
                Logger.w("unlock", "archive-commit swapped, but sent no bundle; unsealing the restored vault")
                restoredSink()
                tryUnsealAndSpawn(passphrase, isDefault = false)
            }
            VaultHelper.CommitResult.WrongPassphrase -> Outcome.WrongPassphrase
            VaultHelper.CommitResult.NotApplied -> {
                Logger.w("unlock", "archive-commit: not applied; see haoma-vault.log")
                Outcome.RestoreFailed(RESTORE_NOT_APPLIED)
            }
            VaultHelper.CommitResult.StoreInUse -> {
                Logger.w("unlock", "archive-commit: a daemon still has its store open after the stop; see haoma-vault.log")
                Outcome.RestoreFailed(RESTORE_STORE_IN_USE)
            }
            VaultHelper.CommitResult.Pending -> {
                restorePendingSink()
                Outcome.RestoreFailed(RESTORE_PENDING)
            }
            VaultHelper.CommitResult.NotEmpty -> {
                Logger.w("unlock", "archive-commit: refused, the device is not empty")
                Outcome.RestoreFailed(RESTORE_NOT_EMPTY)
            }
            VaultHelper.CommitResult.Broken -> {
                Logger.e("unlock", "archive-commit: failed part way; see haoma-vault.log")
                restoredSink()
                Outcome.RestoreFailed(RESTORE_BROKEN)
            }
        }
    }

    private suspend fun tryUnsealAndSpawn(passphrase: ByteArray, isDefault: Boolean): Outcome {
        val unsealed = try {
            VaultHelper.unseal(app, passphrase)
        } catch (e: CancellationException) {
            throw e
        } catch (e: VaultHelper.RestorePendingException) {
            
            
            restorePendingSink()
            return Outcome.RestoreFailed(RESTORE_PENDING)
        } catch (t: Throwable) {
            
            
            return if (isDefault) {
                Outcome.NeedsPassphrase
            } else {
                Outcome.WrongPassphrase
            }
        }
        return finishUnseal(passphrase, unsealed)
    }

    private suspend fun finishUnseal(
        passphrase: ByteArray,
        unsealed: VaultHelper.Unsealed,
    ): Outcome {
        
        
        policySink(unsealed.policy)
        
        
        if (unsealed.payload.isNotEmpty()) {
            sessionSink(VaultSession(app, passphrase, unsealed.payload))
        } else {
            Logger.w("unlock", "haoma-vault returned empty payload — VaultSession not installed; vault writes disabled this session")
            sessionSink(null)
        }
        passphraseDefaultSink(constantTimeEquals(passphrase, VaultHelper.DefaultPassphraseBytes))
        
        
        val ack = BootstrapPayload.deposit(unsealed.secrets)
        HaomaCoreService.start(app)
        val result = try {
            withTimeout(SPAWN_TIMEOUT_MS) { ack.await() }
        } catch (t: Throwable) {
            return Outcome.SpawnFailed("ack await: ${t.message}")
        }
        return when (result) {
            is BootstrapPayload.Result.Ok -> Outcome.Warmed
            is BootstrapPayload.Result.Fail -> Outcome.SpawnFailed(result.message)
        }
    }

    companion object {
        private const val SPAWN_TIMEOUT_MS = 35_000L

        
        private const val RESTORE_STOP_TIMEOUT_MS = 8_000L

        
        private const val RESTORE_WRITEBACK_SETTLE_MS = 250L

        const val RESTORE_NOT_APPLIED = "The restore was not applied. This device is unchanged."
        const val RESTORE_BROKEN = VaultHelper.RESTORE_DID_NOT_FINISH
        const val RESTORE_UNKNOWN =
            "The restore did not finish. This device may be part way through it."
        const val RESTORE_STORE_IN_USE =
            "The restore was not applied: the messenger is still running. This device is unchanged."
        const val RESTORE_PENDING = VaultHelper.RESTORE_DID_NOT_FINISH
        const val RESTORE_NOT_EMPTY = VaultHelper.RESTORE_NOT_EMPTY

        
        internal suspend fun stopServiceForRestore(app: Context): Boolean {
            if (!HaomaCoreService.running) return true
            Logger.i("unlock", "restore: stopping the service")
            val signal = HaomaCoreService.stopAndAwait(app)
            withTimeoutOrNull(RESTORE_STOP_TIMEOUT_MS) { signal.await() } ?: return false
            delay(RESTORE_WRITEBACK_SETTLE_MS)
            return true
        }
    }
}


private fun constantTimeEquals(a: ByteArray, b: ByteArray): Boolean {
    val la = a.size
    val lb = b.size
    var diff = la xor lb
    val n = maxOf(la, lb)
    for (i in 0 until n) {
        val ca = if (i < la) a[i].toInt() and 0xFF else 0
        val cb = if (i < lb) b[i].toInt() and 0xFF else 0
        diff = diff or (ca xor cb)
    }
    return diff == 0
}


internal sealed interface RestoreAttempt {
    
    data class Done(val result: VaultHelper.CommitResult) : RestoreAttempt

    
    data object StopFailed : RestoreAttempt
}


internal suspend fun commitStoppingDaemons(
    commit: () -> VaultHelper.CommitResult,
    stopDaemons: suspend () -> Boolean,
): RestoreAttempt {
    val first = commit()
    if (first != VaultHelper.CommitResult.StoreInUse) return RestoreAttempt.Done(first)
    Logger.i("unlock", "restore: a store is open; stopping the service and committing again")
    if (!stopDaemons()) return RestoreAttempt.StopFailed
    return RestoreAttempt.Done(commit())
}
