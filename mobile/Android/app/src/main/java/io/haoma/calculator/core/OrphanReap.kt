package io.haoma.calculator.core

import android.os.Process
import io.haoma.calculator.log.Logger
import java.io.File
import org.json.JSONObject


private const val ORPHAN_GRACE_MS = 3000L
private const val HAOMAD_RUNTIME_FILE = "haomad.runtime.json"
private const val HAOMA_PID_FILE = "haoma.pid"


fun reapHaomadOrphan(cfgDir: File) {
    reapFromJsonPidfile(File(cfgDir, HAOMAD_RUNTIME_FILE), label = "haomad") { json ->
        json.optInt("pid", -1).takeIf { it > 0 }
    }
}


fun reapHaomaOrphan(cfgDir: File) {
    reapFromPlainPidfile(File(cfgDir, HAOMA_PID_FILE), label = "haoma")
}


fun reapDaemonOrphans(cfgDir: File) {
    reapHaomadOrphan(cfgDir)
    reapHaomaOrphan(cfgDir)
}

private fun reapFromJsonPidfile(
    file: File,
    label: String,
    pickPid: (JSONObject) -> Int?,
) {
    if (!file.exists()) return
    val pid = runCatching { pickPid(JSONObject(file.readText())) }.getOrNull()
    reapPid(pid, label, file)
}

private fun reapFromPlainPidfile(file: File, label: String) {
    if (!file.exists()) return
    val pid = runCatching { file.readText().trim().toInt() }.getOrNull()
    reapPid(pid, label, file)
}

private fun reapPid(pid: Int?, label: String, file: File) {
    if (pid != null && pid > 0 && pid != Process.myPid()) {
        Logger.i("orphan-reap", "label=$label pid=$pid file=${file.name}")
        val ok = Daemon.stop(pid, ORPHAN_GRACE_MS)
        if (!ok) Logger.w("orphan-reap", "label=$label pid=$pid still alive after SIGKILL")
    }
    runCatching { file.delete() }
}
