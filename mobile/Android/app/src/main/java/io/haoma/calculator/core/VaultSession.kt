package io.haoma.calculator.core

import android.content.Context
import io.haoma.calculator.log.Logger
import org.json.JSONObject


class VaultSession(
    private val context: Context,
    initialPassphrase: ByteArray,
    initialPayload: ByteArray,
) {
    private val mutex = Object()

    
    @Volatile
    private var passphrase: ByteArray = initialPassphrase.copyOf()

    @Volatile
    private var payload: JSONObject = JSONObject(initialPayload.toString(Charsets.UTF_8))

    init {
        Logger.d("vault", "session created pass_len=${passphrase.size}")
    }

    
    fun snapshot(): JSONObject {
        synchronized(mutex) {
            return JSONObject(payload.toString())
        }
    }

    
    fun setTorPassword(newValue: String) {
        synchronized(mutex) {
            val prev = payload.optString("tor_password", "")
            payload.put("tor_password", newValue)
            try {
                resealLocked()
                Logger.i("vault", "tor_password updated")
            } catch (t: Throwable) {
                payload.put("tor_password", prev)
                throw t
            }
        }
    }

    
    fun put(key: String, value: Any?) {
        synchronized(mutex) {
            val prev: Any? = if (payload.has(key)) payload.get(key) else null
            payload.put(key, value)
            try {
                resealLocked()
                Logger.i("vault", "field updated key=$key")
            } catch (t: Throwable) {
                if (prev == null) {
                    payload.remove(key)
                } else {
                    payload.put(key, prev)
                }
                throw t
            }
        }
    }

    
    fun mutateAndReseal(label: String, transform: (JSONObject) -> Unit) {
        synchronized(mutex) {
            val before = JSONObject(payload.toString())
            transform(payload)
            try {
                resealLocked()
                Logger.i("vault", "mutate ok label=$label")
            } catch (t: Throwable) {
                payload = before
                throw t
            }
        }
    }

    
    fun changePassphrase(oldPass: ByteArray, newPass: ByteArray) {
        require(newPass.isNotEmpty()) { "new passphrase must not be empty" }
        synchronized(mutex) {
            if (!constantTimeEquals(oldPass, passphrase)) {
                throw IllegalArgumentException("current passphrase does not match")
            }
            val prev = passphrase
            
            
            passphrase = newPass.copyOf()
            try {
                resealLocked()
                java.util.Arrays.fill(prev, 0)
                Logger.i("vault", "passphrase rotated")
            } catch (t: Throwable) {
                
                
                java.util.Arrays.fill(passphrase, 0)
                passphrase = prev
                throw t
            }
        }
    }

    
    fun isPassphraseDefault(): Boolean {
        synchronized(mutex) {
            return constantTimeEquals(passphrase, VaultHelper.DefaultPassphraseBytes)
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

    
    fun secretsForRestart(context: Context): ByteArray {
        synchronized(mutex) {
            return VaultHelper.unseal(context, passphrase).secrets
        }
    }

    
    fun wipe() {
        synchronized(mutex) {
            if (passphrase.isEmpty()) return
            val started = System.nanoTime()
            java.util.Arrays.fill(passphrase, 0)
            passphrase = ByteArray(0)
            val durUs = (System.nanoTime() - started) / 1_000L
            Logger.d("vault", "session wipe fired dur_us=$durUs")
        }
    }

    
    private fun resealLocked() {
        val bytes = payload.toString().toByteArray(Charsets.UTF_8)
        val started = System.currentTimeMillis()
        VaultHelper.reseal(context, bytes, passphrase)
        val dur = System.currentTimeMillis() - started
        Logger.i("vault", "reseal subprocess ok dur_ms=$dur bytes=${bytes.size}")
    }
}
