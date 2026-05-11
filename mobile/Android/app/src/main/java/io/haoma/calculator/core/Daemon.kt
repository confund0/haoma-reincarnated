package io.haoma.calculator.core

import io.haoma.calculator.log.Logger
import java.io.BufferedReader
import java.io.File
import java.io.IOException
import java.io.InputStreamReader
import java.util.concurrent.CompletableFuture
import java.util.concurrent.TimeUnit
import java.util.concurrent.TimeoutException
import org.json.JSONObject


class Daemon private constructor(
    val name: String,
    private val process: Process,
) {
    private val ready = CompletableFuture<String>()

    init {
        Thread(::runReader, "daemon-stdout-$name").apply { isDaemon = true; start() }
        Thread(::runReaper, "daemon-reap-$name").apply { isDaemon = true; start() }
    }

    
    fun waitReady(timeoutMs: Long): String {
        return try {
            ready.get(timeoutMs, TimeUnit.MILLISECONDS)
        } catch (e: TimeoutException) {
            throw IOException("$name: WaitReady timed out after ${timeoutMs}ms", e)
        } catch (e: java.util.concurrent.ExecutionException) {
            
            throw e.cause ?: e
        }
    }

    
    fun stop(graceMs: Long): Int {
        if (!process.isAlive) return process.exitValue()
        Logger.i("daemon", "$name stop (grace=${graceMs}ms)")
        process.destroy()
        if (!process.waitFor(graceMs, TimeUnit.MILLISECONDS)) {
            Logger.w("daemon", "$name did not exit within ${graceMs}ms; SIGKILL")
            process.destroyForcibly()
            process.waitFor()
        }
        return process.exitValue()
    }

    
    val isAlive: Boolean get() = process.isAlive

    private fun runReader() {
        var delivered = false
        try {
            BufferedReader(InputStreamReader(process.inputStream, Charsets.UTF_8)).use { br ->
                while (true) {
                    val line = br.readLine() ?: break
                    if (!delivered) {
                        val (addr, err) = parseReadyLine(line)
                        if (addr != null) {
                            Logger.i("daemon", "$name ready api_addr=$addr")
                            ready.complete(addr)
                        } else {
                            ready.completeExceptionally(
                                IOException("$name stdout: expected ready-line, got '$line': $err"),
                            )
                        }
                        delivered = true
                        continue
                    }
                    
                    
                }
            }
        } catch (e: IOException) {
            if (!delivered) {
                ready.completeExceptionally(IOException("$name stdout reader: ${e.message}", e))
            }
        }
        if (!delivered) {
            ready.completeExceptionally(IOException("$name: stdout closed before ready-line"))
        }
    }

    private fun runReaper() {
        val exit = try {
            process.waitFor()
        } catch (e: InterruptedException) {
            Logger.w("daemon", "$name reaper interrupted")
            return
        }
        Logger.i("daemon", "$name exited code=$exit")
        if (!ready.isDone) {
            ready.completeExceptionally(IOException("$name exited code=$exit before ready-line"))
        }
    }

    companion object {
        

        fun spawn(
            name: String,
            bin: File,
            args: List<String>,
            secretsBlob: ByteArray,
            errLog: File,
        ): Daemon {
            require(bin.exists()) { "$name binary missing at ${bin.absolutePath}" }
            Logger.i("daemon", "spawn $name ${bin.absolutePath} args=$args")
            val cmd = mutableListOf(bin.absolutePath).apply { addAll(args) }
            val proc = ProcessBuilder(cmd)
                .redirectError(ProcessBuilder.Redirect.appendTo(errLog))
                .start()
            try {
                proc.outputStream.use { it.write(secretsBlob) }
            } catch (e: IOException) {
                proc.destroyForcibly()
                throw IOException("$name: write secrets to stdin: ${e.message}", e)
            }
            return Daemon(name, proc)
        }
    }
}

private fun parseReadyLine(line: String): Pair<String?, String?> {
    val trimmed = line.trim()
    if (trimmed.isEmpty()) return null to "empty"
    if (!trimmed.startsWith("{")) return null to "not a JSON object"
    return try {
        val obj = JSONObject(trimmed)
        val status = obj.optString("status")
        if (status != "ready") return null to "status='$status' (want 'ready')"
        val addr = obj.optString("api_addr").trim()
        if (addr.isEmpty()) return null to "api_addr empty"
        addr to null
    } catch (e: Exception) {
        null to "decode: ${e.message}"
    }
}
