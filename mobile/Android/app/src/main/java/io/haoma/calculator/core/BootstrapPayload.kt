package io.haoma.calculator.core

import io.haoma.calculator.log.Logger
import kotlinx.coroutines.CompletableDeferred


object BootstrapPayload {
    sealed interface Result {
        

        data class Ok(val haomadApiAddr: String, val haomaApiAddr: String) : Result
        data class Fail(val message: String) : Result
    }

    private val lock = Any()

    private data class Slot(
        val secrets: ByteArray,
        val ack: CompletableDeferred<Result>,
    )

    @Volatile
    private var slot: Slot? = null

    
    fun deposit(secrets: ByteArray): CompletableDeferred<Result> {
        val ack = CompletableDeferred<Result>()
        val superseded = synchronized(lock) {
            val previous = slot
            slot = Slot(secrets, ack)
            previous
        }
        if (superseded != null) {
            Logger.w("bootstrap-payload", "deposit superseded prior uncompleted slot")
            superseded.secrets.fill(0)
            superseded.ack.complete(Result.Fail("superseded by re-deposit"))
        }
        return ack
    }

    
    internal fun take(): Pair<ByteArray, CompletableDeferred<Result>>? {
        val s = synchronized(lock) { slot.also { slot = null } } ?: return null
        return s.secrets to s.ack
    }
}
