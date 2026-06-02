package io.haoma.calculator.core

import android.os.SystemClock
import android.system.ErrnoException
import android.system.Os
import android.system.OsConstants
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
    private val pidFile: File? = null,
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
        if (!process.isAlive) {
            deletePidFile()
            return process.exitValue()
        }
        Logger.i("daemon", "$name stop (grace=${graceMs}ms)")
        process.destroy()
        if (!process.waitFor(graceMs, TimeUnit.MILLISECONDS)) {
            Logger.w("daemon", "$name did not exit within ${graceMs}ms; SIGKILL")
            process.destroyForcibly()
            process.waitFor()
        }
        deletePidFile()
        return process.exitValue()
    }

    private fun deletePidFile() {
        val f = pidFile ?: return
        runCatching { f.delete() }
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
            pidFile: File? = null,
        ): Daemon {
            require(bin.exists()) { "$name binary missing at ${bin.absolutePath}" }
            Logger.i("daemon", "spawn $name ${bin.absolutePath} args=$args")
            val cmd = mutableListOf(bin.absolutePath).apply { addAll(args) }
            val proc = ProcessBuilder(cmd)
                .redirectError(ProcessBuilder.Redirect.appendTo(errLog))
                .start()
            if (pidFile != null) {
                runCatching {
                    pidFile.parentFile?.mkdirs()
                    pidFile.writeText("${childPidOf(proc)}\n")
                }.onFailure { Logger.w("daemon", "$name pidfile write failed: ${it.message}") }
            }
            try {
                proc.outputStream.use { it.write(secretsBlob) }
            } catch (e: IOException) {
                proc.destroyForcibly()
                if (pidFile != null) runCatching { pidFile.delete() }
                throw IOException("$name: write secrets to stdin: ${e.message}", e)
            }
            return Daemon(name, proc, pidFile)
        }

        
        fun stop(pid: Int, graceMs: Long): Boolean {
            if (!isAlive(pid)) return true
            Logger.i("daemon", "stop pid=$pid (grace=${graceMs}ms)")
            runCatching { Os.kill(pid, OsConstants.SIGTERM) }
            val deadline = SystemClock.elapsedRealtime() + graceMs
            while (SystemClock.elapsedRealtime() < deadline) {
                if (!isAlive(pid)) return true
                Thread.sleep(50)
            }
            Logger.w("daemon", "pid=$pid did not exit within ${graceMs}ms; SIGKILL")
            runCatching { Os.kill(pid, OsConstants.SIGKILL) }
            Thread.sleep(50)
            return !isAlive(pid)
        }

        private fun isAlive(pid: Int): Boolean = try {
            Os.kill(pid, 0)  
            true
        } catch (e: ErrnoException) {
            
            
            e.errno != OsConstants.ESRCH
        }
    }
}


private fun childPidOf(p: Process): Long {
    runCatching {
        return p.javaClass.getMethod("pid").invoke(p) as Long
    }
    val m = Regex("pid=(\\d+)").find(p.toString())
    return m?.groupValues?.get(1)?.toLongOrNull()
        ?: error("could not determine PID from ${p.javaClass.name}: ${p.toString().take(120)}")
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
