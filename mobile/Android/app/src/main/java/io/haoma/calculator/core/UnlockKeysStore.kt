package io.haoma.calculator.core

import android.content.Context
import android.util.Base64
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.UnlockKeySettings
import java.io.File
import org.json.JSONObject


class UnlockKeysStore internal constructor(
    private val file: File,
    private val legacyFile: File?,
) {
    constructor(context: Context) : this(
        file = File(VaultHelper.cfgDir(context), DEFAULT_FILE_NAME),
        legacyFile = File(context.filesDir, DEFAULT_FILE_NAME),
    )

    
    fun migrateLegacy() {
        val legacy = legacyFile ?: return
        when (moveLegacyFile(legacy, file)) {
            LegacyMove.Moved -> Logger.i("unlock-keys", "sidecar moved into the data dir")
            LegacyMove.OldDropped -> Logger.i("unlock-keys", "old sidecar dropped; the data dir already has one")
            LegacyMove.Failed -> Logger.w("unlock-keys", "moving the sidecar into the data dir failed; it stays in the old place")
            LegacyMove.Nothing -> Unit
        }
    }

    
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
            
            
            Logger.w("unlock-keys", "load failed (${t.javaClass.simpleName}) — falling back to defaults")
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
        ensureOwnerOnlyDir(file.parentFile)
        val tmp = File(file.parentFile, "${file.name}.tmp")
        tmp.writeText(payload, Charsets.UTF_8)
        if (!tmp.renameTo(file)) {
            
            file.writeText(payload, Charsets.UTF_8)
            tmp.delete()
        }
        
        
        Logger.d(
            "unlock-keys",
            "saved pattern=${slot(settings.patternKey)} pin=${slot(settings.pinKey)} " +
                "bypass=${slot(settings.bypassKey)}",
        )
    }

    
    internal enum class LegacyMove { Nothing, Moved, OldDropped, Failed }

    companion object {
        const val DEFAULT_FILE_NAME = "calc-help.txt"

        
        internal fun moveLegacyFile(legacy: File, target: File): LegacyMove {
            if (!legacy.exists()) return LegacyMove.Nothing
            if (target.exists()) {
                legacy.delete()
                return LegacyMove.OldDropped
            }
            ensureOwnerOnlyDir(target.parentFile)
            if (legacy.renameTo(target)) return LegacyMove.Moved
            
            
            val tmp = File(target.parentFile, "${target.name}.tmp")
            return try {
                legacy.copyTo(tmp, overwrite = true)
                if (!tmp.renameTo(target)) error("rename ${tmp.name}")
                legacy.delete()
                LegacyMove.Moved
            } catch (t: Throwable) {
                tmp.delete()
                LegacyMove.Failed
            }
        }

        
        internal fun ensureOwnerOnlyDir(dir: File?) {
            if (dir == null || dir.exists()) return
            dir.mkdirs()
            dir.setReadable(false, false)
            dir.setWritable(false, false)
            dir.setExecutable(false, false)
            dir.setReadable(true, true)
            dir.setWritable(true, true)
            dir.setExecutable(true, true)
        }

        
        internal fun slot(key: String): String = if (key.isEmpty()) "off" else "set"

        val DEFAULTS = UnlockKeySettings(patternKey = "5", pinKey = "1", bypassKey = "")
    }
}
