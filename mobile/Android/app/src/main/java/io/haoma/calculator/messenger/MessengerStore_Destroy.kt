package io.haoma.calculator.messenger

import android.content.Context
import android.os.Process
import io.haoma.calculator.core.HaomaCoreService
import io.haoma.calculator.log.Logger
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull


const val DESTROY_SCREEN_GRACE_MS = 2_500L


const val DESTROY_REASON_UNSTATED = "the other device commanded it"


data class DestroyTimings(
    val screenGraceMs: Long = DESTROY_SCREEN_GRACE_MS,
    val clearLiveState: suspend (MessengerStore) -> Unit = { it.clearLiveStateForErase() },
    val tearDown: suspend (Context?) -> Unit = ::stopAndExit,
)


const val ERASE_STOP_TIMEOUT_MS = HaomaCoreService.HAOMAD_ERASE_EXIT_TIMEOUT_MS + 10_000L


internal suspend fun stopAndExit(ctx: Context?) {
    if (ctx == null) {
        Logger.w("destroy", "no app context, cannot stop the daemons before ending this process")
        return
    }
    try {
        val stopped = withTimeoutOrNull(ERASE_STOP_TIMEOUT_MS) {
            HaomaCoreService.stopForErase(ctx).await()
        }
        if (stopped == null) {
            Logger.w("destroy", "the daemons did not report stopped in time; ending this process anyway")
        }
    } catch (t: Throwable) {
        Logger.w("destroy", "stopping the daemons failed (${t.message ?: "?"}); ending this process anyway")
    }
    Logger.i("destroy", "the daemons are stopped; this process ends")
    Process.killProcess(Process.myPid())
}


internal fun destroyDeviceSentence(reason: String): String {
    val stated = reason.trim().ifEmpty { DESTROY_REASON_UNSTATED }
    return "This device is being erased: $stated"
}


internal suspend fun runDestroy(
    reason: String,
    showSentence: (String) -> Unit,
    clearLiveState: suspend () -> Unit,
    graceMs: Long,
    tearDown: suspend () -> Unit,
    eraseId: String = "",
) {
    showSentence(destroyDeviceSentence(reason))
    
    
    val eraseTag = if (eraseId.length > 8) eraseId.take(8) + "…" else eraseId
    Logger.i("destroy", "the reason is on screen erase_id=$eraseTag")
    Logger.i("destroy", "clearing the unlock keys, the clipboard and the call banner erase_id=$eraseTag")
    clearLiveState()
    delay(graceMs)
    tearDown()
}


fun MessengerStore.onDestroyDevice(push: DestroyDevicePush) {
    if (!destroyStarted.compareAndSet(false, true)) {
        Logger.i(
            "destroy",
            "destroy_device again while the teardown is running; ignored erase_id=${push.eraseId}",
        )
        return
    }
    Logger.i(
        "destroy",
        "destroy_device received; showing the reason, then stopping erase_id=${push.eraseId}",
    )
    scope.launch {
        runDestroy(
            reason = push.reason,
            showSentence = { sentence ->
                _destroySentence.value = sentence
                appendStatus(sentence, level = StatusLevel.WARN)
            },
            clearLiveState = { destroyTimings.clearLiveState(this@onDestroyDevice) },
            graceMs = destroyTimings.screenGraceMs,
            tearDown = {
                Logger.i(
                    "destroy",
                    "reason shown; stopping haoma, waiting for haomad, ending this process " +
                        "erase_id=${push.eraseId}",
                )
                destroyTimings.tearDown(appContext)
            },
            eraseId = push.eraseId,
        )
    }
}


fun MessengerStore.onDestroyAttemptsSeen(push: DestroyAttemptsSeenPush) {
    Logger.i("destroy", "destroy_attempts_seen count=${push.count} last_at=${push.lastAt}")
    _noticeDestroyAttempts.value = push
}


internal fun destroyAttemptsBody(count: Int, lastAt: Long): String {
    val many = if (count == 1) "One wrong attempt." else "$count wrong attempts."
    val head = "Somebody tried the destroy PIN on this device. $many"
    if (lastAt <= 0L) return head
    val when_ = SimpleDateFormat("yyyy-MM-dd HH:mm", Locale.US).format(Date(lastAt * 1000L))
    val tail = if (count == 1) "It was at $when_." else "The last one was at $when_."
    return "$head $tail"
}
