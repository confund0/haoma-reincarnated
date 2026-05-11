package io.haoma.calculator.core

import android.content.Context
import io.haoma.calculator.log.Logger
import io.haoma.disguise.AppState
import io.haoma.disguise.AppStateRepository
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.coroutines.cancellation.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout


class UnlockManager(
    private val app: Context,
    private val state: AppStateRepository,
    private val policySink: (IdlePolicy) -> Unit = {},
    private val sessionSink: (VaultSession?) -> Unit = {},
) {
    sealed interface Outcome {
        data object Warmed : Outcome

        
        data object NeedsPassphrase : Outcome

        
        data class SpawnFailed(val message: String) : Outcome

        
        data object WrongPassphrase : Outcome
    }

    private val inFlight = AtomicBoolean(false)

    
    fun handleHardSlideMatch() {
        Logger.i("unlock", "slide match → Passphrase (always-prompt UX)")
        state.update(AppState.Locked.Passphrase)
    }

    
    suspend fun submitPassphrase(passphrase: String): Outcome =
        runUnseal(passphrase)

    
    suspend fun submitDefaultPassphrase(): Outcome =
        runUnseal(VaultHelper.DefaultPassphrase)

    private suspend fun runUnseal(passphrase: String): Outcome {
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

    
    fun revertToHard() {
        Logger.i("unlock", "revert → Hard")
        state.update(AppState.Locked.Hard)
    }

    private suspend fun tryUnsealAndSpawn(passphrase: String, isDefault: Boolean): Outcome {
        val unsealed = try {
            VaultHelper.unseal(app, passphrase)
        } catch (e: CancellationException) {
            throw e
        } catch (t: Throwable) {
            
            
            return if (isDefault) {
                Outcome.NeedsPassphrase
            } else {
                Outcome.WrongPassphrase
            }
        }
        
        
        policySink(unsealed.policy)
        
        
        if (unsealed.payload.isNotEmpty()) {
            sessionSink(VaultSession(app, passphrase, unsealed.payload))
        } else {
            Logger.w("unlock", "haoma-vault returned empty payload — VaultSession not installed; vault writes disabled this session")
            sessionSink(null)
        }
        
        
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
    }
}
