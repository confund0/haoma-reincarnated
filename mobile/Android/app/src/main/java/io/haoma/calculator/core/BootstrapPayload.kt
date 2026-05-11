package io.haoma.calculator.core

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
        synchronized(lock) {
            require(slot == null) { "BootstrapPayload: slot already occupied" }
            slot = Slot(secrets, ack)
        }
        return ack
    }

    
    internal fun take(): Pair<ByteArray, CompletableDeferred<Result>>? {
        val s = synchronized(lock) { slot.also { slot = null } } ?: return null
        return s.secrets to s.ack
    }
}
