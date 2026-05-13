package io.haoma.calculator.log

import android.content.Context
import java.io.File
import java.io.FileOutputStream
import java.io.OutputStreamWriter
import java.io.PrintWriter
import java.io.StringWriter
import java.io.Writer
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.TimeZone
import kotlinx.coroutines.CoroutineExceptionHandler

object Logger {
    private const val FILE = "haoma-gui.log"
    private const val PREV = "haoma-gui.log.prev"
    private const val MAX_BYTES: Long = 4L shl 20

    private val TS = SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss.SSS'Z'", Locale.US).apply {
        timeZone = TimeZone.getTimeZone("UTC")
    }

    private val lock = Any()
    private val rotated = HashSet<String>()

    @Volatile private var writer: Writer? = null
    @Volatile private var current: File? = null
    @Volatile private var prev: File? = null
    @Volatile private var size: Long = 0
    @Volatile private var dir: File? = null
    @Volatile private var debugBuild: Boolean = false

    
    val suiteLogLevel: String get() = if (debugBuild) "debug" else "warn"

    fun init(context: Context, debug: Boolean) {
        synchronized(lock) {
            if (writer != null) return
            val target = if (debug) {
                File(context.getExternalFilesDir(null) ?: context.filesDir, "logs")
            } else {
                File(context.filesDir, "logs")
            }
            target.mkdirs()
            val cur = File(target, FILE)
            val prv = File(target, PREV)
            rotateInPlace(cur, prv)
            rotated.add("haoma-gui")
            writer = OutputStreamWriter(FileOutputStream(cur, true), Charsets.UTF_8)
            current = cur
            prev = prv
            size = cur.length()
            dir = target
            debugBuild = debug
        }
        write("INFO", "logger", "log file opened debug=$debug dir=${dir?.absolutePath} suiteLogLevel=$suiteLogLevel")
    }

    
    fun fileFor(component: String): String {
        require(component != "haoma-gui") {
            "haoma-gui is owned by Logger writer; do not request its path"
        }
        val d = dir ?: error("Logger.init not called")
        val f = File(d, "$component.log")
        synchronized(lock) {
            if (rotated.add(component)) {
                rotateInPlace(f, File(d, "$component.log.prev"))
            }
        }
        return f.absolutePath
    }

    private fun rotateInPlace(current: File, prev: File) {
        if (current.exists()) {
            if (prev.exists()) prev.delete()
            current.renameTo(prev)
        }
    }

    fun write(level: String, tag: String, msg: String) {
        if (writer == null) return
        val line = "${TS.format(Date())} $level $tag $msg\n"
        val bytes = line.toByteArray(Charsets.UTF_8).size.toLong()
        try {
            synchronized(lock) {
                val w = writer ?: return
                if (size > 0 && size + bytes > MAX_BYTES) {
                    rotateCurrentLocked()
                }
                val w2 = writer ?: return
                w2.write(line)
                w2.flush()
                size += bytes
            }
        } catch (_: Throwable) {
            
        }
    }

    private fun rotateCurrentLocked() {
        val cur = current ?: return
        val prv = prev ?: return
        try {
            writer?.flush()
            writer?.close()
        } catch (_: Throwable) {
            
        }
        rotateInPlace(cur, prv)
        writer = OutputStreamWriter(FileOutputStream(cur, true), Charsets.UTF_8)
        size = 0
    }

    fun d(tag: String, msg: String) = write("DEBUG", tag, msg)
    fun i(tag: String, msg: String) = write("INFO", tag, msg)
    fun w(tag: String, msg: String) = write("WARN", tag, msg)
    fun e(tag: String, msg: String, t: Throwable? = null) {
        if (t == null) write("ERROR", tag, msg)
        else write("ERROR", tag, "$msg\n${stackTrace(t)}")
    }

    fun stackTrace(t: Throwable): String {
        val sw = StringWriter()
        t.printStackTrace(PrintWriter(sw))
        return sw.toString()
    }

    val coroutineExceptionHandler: CoroutineExceptionHandler =
        CoroutineExceptionHandler { ctx, t -> e("coroutine", "uncaught in $ctx", t) }

    fun logsDir(): File? = dir
}
