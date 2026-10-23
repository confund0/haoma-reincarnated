package io.haoma.calculator.messenger

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.os.Build
import io.haoma.calculator.core.UnlockKeysStore
import io.haoma.calculator.core.ipc.FrameType
import io.haoma.calculator.log.Logger
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext


const val WIPE_CONFIRM_WORD = "ERASE"


internal enum class WipeGuardStep {
    
    WrongWord,

    
    BypassKeyGiven,

    
    CheckUnlockSecret,
}


internal fun wipeGuardStep(word: String, unlockAnswer: String, bypassKey: String): WipeGuardStep {
    if (word.trim() != WIPE_CONFIRM_WORD) return WipeGuardStep.WrongWord
    if (bypassKey.isNotEmpty() && unlockAnswer.trim() == bypassKey) return WipeGuardStep.BypassKeyGiven
    return WipeGuardStep.CheckUnlockSecret
}


data class WipeResult(
    val ok: Boolean,
    val text: String,
)


suspend fun MessengerStore.wipeThisDevice(word: String, unlockAnswer: String): WipeResult {
    when (wipeGuardStep(word, unlockAnswer, unlockKeysStore?.load()?.bypassKey ?: "")) {
        WipeGuardStep.WrongWord -> {
            Logger.i("wipe", "refused: the confirmation word was wrong; the unlock answer was not read")
            return WipeResult(false, WIPE_WRONG_WORD)
        }

        WipeGuardStep.BypassKeyGiven ->
            Logger.i("wipe", "the unlock answer is this device's bypass key")

        WipeGuardStep.CheckUnlockSecret -> {
            val sidecar = disguise
            if (sidecar == null) {
                Logger.w("wipe", "refused: no sidecar to check the unlock answer against")
                return WipeResult(false, WIPE_NO_UNLOCK_CHECK)
            }
            
            
            val accepted = withContext(Dispatchers.IO) { sidecar.verify(unlockAnswer.trim()) }
            if (!accepted) {
                
                Logger.i("wipe", "refused: the unlock answer was wrong")
                return WipeResult(false, WIPE_WRONG_PIN)
            }
        }
    }

    Logger.i("wipe", "both answers accepted; asking the daemon to erase this device")
    return sendWipeRequest()
}


internal suspend fun MessengerStore.sendWipeRequest(): WipeResult {
    val reply = try {
        askDaemon(FrameType.WipeDevice)
    } catch (t: Throwable) {
        Logger.w("wipe", "wipe_device got no answer; the erase may have started: ${t.message ?: "?"}")
        return WipeResult(false, WIPE_NO_ANSWER)
    }
    if (reply.type == FrameType.Error) {
        Logger.w("wipe", "the daemon refused the request; this device is unchanged")
        return WipeResult(false, WIPE_REFUSED)
    }
    if (reply.type != FrameType.DeviceWiping || reply.payload == null) {
        Logger.w("wipe", "the answer to wipe_device could not be read; the erase may have started")
        return WipeResult(false, WIPE_NO_ANSWER)
    }
    if (!DeviceWipingResponse.fromJson(reply.payload).started) {
        Logger.w("wipe", "the daemon says the erasure did not start; this device is unchanged")
        return WipeResult(false, WIPE_REFUSED)
    }
    Logger.i("wipe", "the erasure has started")
    return WipeResult(true, WIPE_STARTED)
}


internal fun MessengerStore.clearLiveStateForErase() {
    try {
        revealKeysUpdater(UnlockKeysStore.DEFAULTS)
        Logger.i("wipe", "the calc skin's unlock keys are back to the factory picks")
    } catch (t: Throwable) {
        Logger.e("wipe", "putting the unlock keys back to the factory picks failed", t)
    }
    val ctx = appContext
    if (ctx == null) {
        Logger.w("wipe", "no app context; the clipboard is not cleared")
    } else {
        clearClipboard(ctx)
    }
    notificationPoster?.cancelCallBanners()
}


private fun clearClipboard(ctx: Context) {
    val clipboard = ctx.getSystemService(ClipboardManager::class.java)
    if (clipboard == null) {
        Logger.w("wipe", "no clipboard service; the clipboard is not cleared")
        return
    }
    try {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            clipboard.clearPrimaryClip()
        } else {
            clipboard.setPrimaryClip(ClipData.newPlainText("", ""))
        }
        Logger.i("wipe", "the clipboard is cleared")
    } catch (t: Throwable) {
        Logger.w("wipe", "clearing the clipboard failed: ${t.message ?: "?"}")
    }
}


const val WIPE_BLOCK_LABEL = "Erase this device"


const val WIPE_BLOCK_PATH = "Settings → Advanced → Erase"


const val WIPE_BODY_GOES = "Erase this device:"
val WIPE_BODY_ITEMS = listOf("identity & contacts", "chats & attachments", "passphrase & PIN")


const val WIPE_BODY_NO_UNDO = "It cannot be undone. There is no backup."


const val WIPE_BODY_STAYS =
    "After the erase this device is empty, like a new install.\n" +
        "Your other devices stay intact.\n" +
        "This app closes when the erase starts."
const val WIPE_BODY_SAVED_FILES =
    "Attachments you saved manually will remain where they are."

const val WIPE_FIELD_UNLOCK = "Unlock PIN"
const val WIPE_FIELD_WORD = "Type $WIPE_CONFIRM_WORD"


const val WIPE_HINT_UNLOCK = "Your PIN, which is the same value as your slide pattern."


const val WIPE_HINT_BYPASS = "$WIPE_HINT_UNLOCK Your bypass key is also accepted."

const val WIPE_ACTION = "Erase this device"

const val WIPE_WRONG_WORD = "Not erased. Type $WIPE_CONFIRM_WORD in capitals."
const val WIPE_WRONG_PIN = "Not erased. Wrong PIN."
const val WIPE_NO_UNLOCK_CHECK = "Not erased. This device cannot check the PIN just now."


const val WIPE_ASKING = "The erase request was sent. Waiting for the daemon."


const val WIPE_REFUSED =
    "Not erased. The daemon refused the request. This device is unchanged."


const val WIPE_NO_ANSWER =
    "The erase may have started. Restart this app. A first-run screen means " +
        "it went through. If your chats are still there, it did not."

const val WIPE_STARTED = "Erasing this device…"
