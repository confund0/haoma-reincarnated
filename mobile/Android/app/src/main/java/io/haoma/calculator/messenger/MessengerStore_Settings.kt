package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.Frame
import io.haoma.calculator.core.ipc.FrameType
import io.haoma.calculator.log.Logger
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONObject


fun MessengerStore.pushSettingsSync() {
    val c = ipc ?: return
    val session = vaultSessionProvider() ?: return
    val snap = session.snapshot()
    val payload = SettingsSnapshot(
        defaultRetentionSec = snap.optLong("default_retention_sec", 0L),
        defaultSendReceipts = snap.optBoolean("default_send_receipts", true),
        idleAction = snap.optString("idle_action", ""),
        idleTimeoutSeconds = snap.optInt("idle_timeout_seconds", 0),
        pinValiditySec = snap.optInt("pin_validity_sec", 0),
        notifyShellEnabled = snap.optBoolean("notify_shell_enabled", false),
        notifyShowSender = snap.optBoolean("notify_show_sender", false),
        notifyShowBody = snap.optBoolean("notify_show_body", false),
        notificationsOnLock = snap.optBoolean("notifications_on_lock", false),
        threatProfile = snap.optString("threat_profile", ""),
        panicAction = snap.optString("panic_action", ""),
        
        hasTorPassword = snap.optString("tor_password", "").isNotEmpty(),
        defaultSaveDir = snap.optString("default_save_dir", ""),
        defaultAttachStartDir = snap.optString("default_attach_start_dir", ""),
    )
    c.send(
        Frame(
            type = FrameType.SyncSettings,
            payload = SyncSettingsRequest(settings = payload).toJson(),
        ),
    )
    Logger.d(
        "messenger",
        "sync_settings emit notify_shell=${payload.notifyShellEnabled} " +
            "show_sender=${payload.notifyShowSender} " +
            "show_body=${payload.notifyShowBody}",
    )
}


private suspend fun MessengerStore.resealVault(
    auditLabel: String,
    successMsg: String,
    failLabel: String = auditLabel,
    pushSync: Boolean = true,
    mutate: (JSONObject) -> Unit,
): Result<Unit> {
    val session = vaultSessionProvider()
        ?: return Result.failure(IllegalStateException("vault session unavailable (re-unlock first)"))
    return try {
        withContext(Dispatchers.IO) { session.mutateAndReseal(auditLabel, mutate) }
        if (pushSync) pushSettingsSync()
        appendStatus(successMsg)
        Result.success(Unit)
    } catch (t: Throwable) {
        appendStatus(
            "$failLabel: vault reseal failed: ${t.message ?: "?"}",
            level = StatusLevel.WARN,
        )
        Result.failure(t)
    }
}


fun MessengerStore.setSelfNick(nick: String) {
    scope.launch {
        val c = ipc ?: run {
            appendStatus("nick: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        val trimmed = nick.trim()
        if (trimmed.isEmpty()) {
            appendStatus("nick: must not be empty", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.SetNick,
                payload = SetNickRequest(trimmed).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("nick error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
                return@launch
            }
            appendStatus("nick → $trimmed")
        } catch (t: Throwable) {
            appendStatus("nick failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }
}


fun MessengerStore.setTorPassword(password: String) {
    scope.launch {
        appendStatus("set-tor-password: re-sealing vault…")
        saveTorPassword(password)
    }
}


fun MessengerStore.loadNotificationSettings(): NotificationSettings? {
    val session = vaultSessionProvider() ?: return null
    val snap = session.snapshot()
    return NotificationSettings(
        shellEnabled = snap.optBoolean("notify_shell_enabled", false),
        showSender = snap.optBoolean("notify_show_sender", false),
        showBody = snap.optBoolean("notify_show_body", false),
        onLock = snap.optBoolean("notifications_on_lock", false),
        disguiseEnabled = snap.optBoolean("notify_disguise_enabled", false),
    )
}


suspend fun MessengerStore.saveNotificationSettings(settings: NotificationSettings): Result<Unit> =
    resealVault("notifications", "notifications saved") { p ->
        p.put("notify_shell_enabled", settings.shellEnabled)
        p.put("notify_show_sender", settings.showSender)
        p.put("notify_show_body", settings.showBody)
        p.put("notifications_on_lock", settings.onLock)
        p.put("notify_disguise_enabled", settings.disguiseEnabled)
    }


fun MessengerStore.loadChatDefaults(): ChatDefaultsSettings? {
    val session = vaultSessionProvider() ?: return null
    val snap = session.snapshot()
    return ChatDefaultsSettings(
        retentionSeconds = snap.optLong("default_retention_sec", 0L),
        sendReceipts = snap.optBoolean("default_send_receipts", true),
    )
}


suspend fun MessengerStore.saveChatDefaults(settings: ChatDefaultsSettings): Result<Unit> =
    resealVault("defaults", successMsg = "chat defaults saved", failLabel = "chat defaults") { p ->
        p.put("default_retention_sec", settings.retentionSeconds)
        p.put("default_send_receipts", settings.sendReceipts)
    }


fun MessengerStore.loadTorSettings(): TorSettings? {
    val session = vaultSessionProvider() ?: return null
    val snap = session.snapshot()
    return TorSettings(hasPassword = snap.optString("tor_password", "").isNotEmpty())
}


suspend fun MessengerStore.saveTorPassword(password: String): Result<Unit> {
    val session = vaultSessionProvider()
        ?: return Result.failure(IllegalStateException("vault session unavailable (re-unlock first)"))
    val c = ipc
        ?: return Result.failure(IllegalStateException("ipc not connected"))
    return try {
        withContext(Dispatchers.IO) { session.setTorPassword(password) }
        
        pushSettingsSync()
        try {
            val reply = c.request(
                type = FrameType.SetTorPassword,
                payload = SetTorPasswordRequest(password).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus(
                    "set-tor-password: live kick failed: ${err?.message ?: "?"} (vault saved)",
                    level = StatusLevel.WARN,
                )
            } else {
                appendStatus("set-tor-password ok — vault saved + haomad kicked")
            }
        } catch (t: Throwable) {
            appendStatus(
                "set-tor-password: ipc kick failed: ${t.message ?: "?"} (vault saved)",
                level = StatusLevel.WARN,
            )
        }
        Result.success(Unit)
    } catch (t: Throwable) {
        appendStatus(
            "set-tor-password: vault reseal failed: ${t.message ?: "?"}",
            level = StatusLevel.WARN,
        )
        Result.failure(t)
    }
}


fun MessengerStore.loadLockSettings(): LockSettings? {
    val session = vaultSessionProvider() ?: return null
    val snap = session.snapshot()
    return LockSettings(
        threatProfile = snap.optString("threat_profile", ""),
        idleAction = snap.optString("idle_action", ""),
        idleTimeoutSeconds = snap.optInt("idle_timeout_seconds", 0),
        pinValiditySec = snap.optInt("pin_validity_sec", 0),
        panicAction = snap.optString("panic_action", ""),
    )
}


fun MessengerStore.loadSecurityWarnings(): List<String>? {
    val session = vaultSessionProvider() ?: return null
    val snap = session.snapshot()
    val arr = snap.optJSONArray("security_warnings") ?: return emptyList()
    return List(arr.length()) { i -> arr.optString(i, "") }.filter { it.isNotEmpty() }
}


suspend fun MessengerStore.saveLock(settings: LockSettings, clearThreatProfile: Boolean): Result<Unit> =
    resealVault("lock", "lock saved") { p ->
        p.put("idle_action", settings.idleAction)
        p.put("idle_timeout_seconds", settings.idleTimeoutSeconds)
        p.put("pin_validity_sec", settings.pinValiditySec)
        p.put("panic_action", settings.panicAction)
        if (clearThreatProfile) p.put("threat_profile", "")
    }


suspend fun MessengerStore.applyThreatPreset(presetId: String): Result<Unit> {
    val bundle = THREAT_PRESET_BUNDLES[presetId]
        ?: return Result.failure(IllegalArgumentException("unknown preset: $presetId"))
    return resealVault("apply-preset", "threat preset applied: $presetId") { p ->
        p.put("threat_profile", presetId)
        p.put("idle_action", bundle.idleAction)
        p.put("idle_timeout_seconds", bundle.idleTimeoutSeconds)
        p.put("pin_validity_sec", bundle.pinValiditySec)
        p.put("panic_action", bundle.panicAction)
    }
}


suspend fun MessengerStore.changeUnlockPattern(oldPattern: String, newPattern: String): Result<Unit> {
    if (newPattern.isEmpty() || newPattern.length < 4 || !newPattern.all { it.isDigit() }) {
        return Result.failure(
            IllegalArgumentException("new pattern must be ≥ 4 digits"),
        )
    }
    val store = disguise
        ?: return Result.failure(IllegalStateException("disguise store unavailable"))
    return try {
        withContext(Dispatchers.IO) {
            store.rekey(oldPattern, newPattern)
        }
        appendStatus("unlock pattern changed")
        Result.success(Unit)
    } catch (t: Throwable) {
        appendStatus(
            "change-pattern: ${t.message ?: "?"}",
            level = StatusLevel.WARN,
        )
        Result.failure(t)
    }
}


suspend fun MessengerStore.changePassphrase(oldPass: String, newPass: String): Result<Unit> {
    val session = vaultSessionProvider()
        ?: return Result.failure(IllegalStateException("vault session unavailable (re-unlock first)"))
    return try {
        withContext(Dispatchers.IO) {
            session.changePassphrase(oldPass, newPass)
        }
        appendStatus("passphrase rotated")
        Result.success(Unit)
    } catch (t: Throwable) {
        appendStatus(
            "change-passphrase: ${t.message ?: "?"}",
            level = StatusLevel.WARN,
        )
        Result.failure(t)
    }
}


data class NotificationSettings(
    val shellEnabled: Boolean,
    val showSender: Boolean,
    val showBody: Boolean,
    val onLock: Boolean,
    val disguiseEnabled: Boolean,
)


data class ChatDefaultsSettings(
    val retentionSeconds: Long,
    val sendReceipts: Boolean,
)


data class TorSettings(
    val hasPassword: Boolean,
)


data class LockSettings(
    val threatProfile: String,
    val idleAction: String,
    val idleTimeoutSeconds: Int,
    val pinValiditySec: Int,
    val panicAction: String,
)


data class ThreatPresetBundle(
    val idleAction: String,
    val idleTimeoutSeconds: Int,
    val pinValiditySec: Int,
    val panicAction: String,
)

internal val THREAT_PRESET_BUNDLES: Map<String, ThreatPresetBundle> = mapOf(
    "domestic" to ThreatPresetBundle(
        idleAction = "soft-lock",
        idleTimeoutSeconds = 300,
        pinValiditySec = 86400,
        panicAction = "safe-lock",
    ),
    "privacy" to ThreatPresetBundle(
        idleAction = "soft-lock",
        idleTimeoutSeconds = 60,
        pinValiditySec = 300,
        panicAction = "hard-lock",
    ),
)
