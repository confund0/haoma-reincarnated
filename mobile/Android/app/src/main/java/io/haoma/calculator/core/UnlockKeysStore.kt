package io.haoma.calculator.core

import android.content.Context
import android.util.Base64
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.UnlockKeySettings
import java.io.File
import org.json.JSONObject


class UnlockKeysStore(
    context: Context,
    fileName: String = DEFAULT_FILE_NAME,
) {
    private val file: File = File(context.filesDir, fileName)

    
    fun load(): UnlockKeySettings {
        if (!file.exists()) {
            Logger.d("unlock-keys", "load default — sidecar absent path=${file.path}")
            return DEFAULTS
        }
        return try {
            val raw = file.readText(Charsets.UTF_8).trim()
            val json = Base64.decode(raw, Base64.DEFAULT)
            val obj = JSONObject(String(json, Charsets.UTF_8))
            UnlockKeySettings(
                patternKey = obj.optString("a", "5"),
                pinKey = obj.optString("b", "1"),
                bypassKey = obj.optString("c", ""),
            )
        } catch (t: Throwable) {
            Logger.w("unlock-keys", "load failed (${t.message}) — falling back to defaults")
            DEFAULTS
        }
    }

    
    fun save(settings: UnlockKeySettings) {
        val obj = JSONObject().apply {
            put("a", settings.patternKey)
            put("b", settings.pinKey)
            put("c", settings.bypassKey)
        }
        val payload = Base64.encodeToString(
            obj.toString().toByteArray(Charsets.UTF_8),
            Base64.NO_WRAP,
        )
        val tmp = File(file.parentFile, "${file.name}.tmp")
        tmp.writeText(payload, Charsets.UTF_8)
        if (!tmp.renameTo(file)) {
            
            file.writeText(payload, Charsets.UTF_8)
            tmp.delete()
        }
        Logger.d("unlock-keys", "saved pattern=${settings.patternKey} pin=${settings.pinKey} " +
            "bypass=${settings.bypassKey.ifEmpty { "(disabled)" }}")
    }

    
    fun clearBypass() {
        val current = load()
        if (current.bypassKey.isEmpty()) return
        save(current.copy(bypassKey = ""))
    }

    companion object {
        const val DEFAULT_FILE_NAME = "calc-help.txt"
        val DEFAULTS = UnlockKeySettings(patternKey = "5", pinKey = "1", bypassKey = "")
    }
}
