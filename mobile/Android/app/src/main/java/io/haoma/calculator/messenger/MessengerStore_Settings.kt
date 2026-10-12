package io.haoma.calculator.messenger

import io.haoma.calculator.core.IdlePolicy
import io.haoma.calculator.core.ipc.Frame
import io.haoma.calculator.core.ipc.FrameType
import io.haoma.calculator.log.Logger
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeoutOrNull
import org.json.JSONObject


fun MessengerStore.pushSettingsSync() {
    val c = ipc ?: return
    val session = vaultSessionProvider() ?: return
    val snap = session.snapshot()
    val payload = SettingsSnapshot(
        idleAction = snap.optString("idle_action", ""),
        idleTimeoutSeconds = snap.optInt("idle_timeout_seconds", 0),
        pinValiditySec = snap.optInt("pin_validity_sec", 0),
        notifyShellEnabled = snap.optBoolean("notify_shell_enabled", false),
        notifyShowSender = snap.optBoolean("notify_show_sender", false),
        notifyShowBody = snap.optBoolean("notify_show_body", false),
        notificationsOnLock = snap.optBoolean("notifications_on_lock", false),
        notifyPersistUntilOpen = snap.optBoolean("notify_persist_until_open", false),
        notifyDeepLink = snap.optBoolean("notify_deep_link", false),
        
        
        showFailedDestroyAttempts = snap.optBoolean("show_failed_destroy_attempts", false),
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


internal fun MessengerStore.refreshVaultDerivedState(session: io.haoma.calculator.core.VaultSession) {
    policyUpdater(IdlePolicy.fromJson(session.snapshot()))
    
    
    loadSaysPassphraseIsDefault(session)
    setPassphraseIsDefault(session.isPassphraseDefault())
    reconcileShareTarget(session)
}


internal suspend fun MessengerStore.resealVault(
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
        refreshVaultDerivedState(session)
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
            
        } catch (t: Throwable) {
            appendStatus("nick failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }
}


fun MessengerStore.setChatFontScale(scale: Float) {
    scope.launch {
        val c = ipc ?: run {
            appendStatus("font-scale: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.SetChatFontScale,
                payload = SetChatFontScaleRequest(scale).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus(
                    "font-scale error: ${err?.message ?: "?"}",
                    level = StatusLevel.WARN,
                )
            }
        } catch (t: Throwable) {
            appendStatus(
                "font-scale failed: ${t.message ?: "?"}",
                level = StatusLevel.WARN,
            )
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
        noisy = snap.optBoolean("notify_noisy", false),
        persistUntilOpen = snap.optBoolean("notify_persist_until_open", false),
        deepLink = snap.optBoolean("notify_deep_link", false),
        showFailedDestroyAttempts = snap.optBoolean("show_failed_destroy_attempts", false),
    )
}


suspend fun MessengerStore.saveNotificationSettings(settings: NotificationSettings): Result<Unit> =
    resealVault("notifications", "notifications saved") { p ->
        p.put("notify_shell_enabled", settings.shellEnabled)
        p.put("notify_show_sender", settings.showSender)
        p.put("notify_show_body", settings.showBody)
        p.put("notifications_on_lock", settings.onLock)
        p.put("notify_disguise_enabled", settings.disguiseEnabled)
        p.put("notify_noisy", settings.noisy)
        p.put("notify_persist_until_open", settings.persistUntilOpen)
        p.put("notify_deep_link", settings.deepLink)
        p.put("show_failed_destroy_attempts", settings.showFailedDestroyAttempts)
    }


fun MessengerStore.loadChatDefaults(): ChatDefaultsSettings? {
    if (ipc == null) return null
    val h = _health.value
    return ChatDefaultsSettings(
        retentionSeconds = h.defaultRetentionSec,
        sendReceipts = h.defaultSendReceipts,
    )
}


fun MessengerStore.chatDefaultsKnown(): Boolean = _health.value.chatDefaultsKnown


suspend fun MessengerStore.saveChatDefaults(settings: ChatDefaultsSettings): Result<Unit> {
    val frame = Frame(
        type = FrameType.SetChatDefaults,
        payload = SetChatDefaultsRequest(settings.retentionSeconds, settings.sendReceipts).toJson(),
    )
    if (!sendFrame(frame)) return Result.failure(IllegalStateException(CHAT_DEFAULTS_NOT_CONNECTED))
    return try {
        val landed = withTimeoutOrNull(CHAT_DEFAULTS_WAIT_MS) {
            health.first { it.chatDefaultsKnown && it.matches(settings) }
        }
        if (landed == null) {
            Logger.w("messenger", "chat defaults not confirmed within ${CHAT_DEFAULTS_WAIT_MS}ms")
            Result.failure(IllegalStateException(CHAT_DEFAULTS_NOT_SAVED))
        } else {
            appendStatus("chat defaults saved")
            Result.success(Unit)
        }
    } catch (t: Throwable) {
        Logger.w("messenger", "chat defaults save failed: ${t.message ?: "?"}")
        Result.failure(IllegalStateException(CHAT_DEFAULTS_NOT_SAVED))
    }
}


internal fun MessengerStore.sendFrame(frame: Frame): Boolean {
    frameSender?.let { return it(frame) }
    val c = ipc ?: return false
    c.send(frame)
    return true
}


private fun SystemHealth.matches(want: ChatDefaultsSettings): Boolean =
    defaultRetentionSec == want.retentionSeconds && defaultSendReceipts == want.sendReceipts


private const val CHAT_DEFAULTS_WAIT_MS = 2_000L


const val CHAT_DEFAULTS_NOT_CONNECTED =
    "Not saved. Haoma is not running yet. Open it and try again."
const val CHAT_DEFAULTS_NOT_SAVED =
    "Not saved. The setting is unchanged. Check Haoma is running, then try again."


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


fun MessengerStore.loadAdvancedSettings(): AdvancedSettings? {
    val session = vaultSessionProvider() ?: return null
    val snap = session.snapshot()
    return AdvancedSettings(
        urlForceChooser = snap.optBoolean("url_force_chooser", true),
    )
}


suspend fun MessengerStore.saveAdvancedSettings(settings: AdvancedSettings): Result<Unit> =
    resealVault("advanced", "advanced settings saved", pushSync = false) { p ->
        p.put("url_force_chooser", settings.urlForceChooser)
    }


fun MessengerStore.currentUrlForceChooser(): Boolean {
    val session = vaultSessionProvider() ?: return true
    return session.snapshot().optBoolean("url_force_chooser", true)
}


suspend fun MessengerStore.saveLock(settings: LockSettings, clearThreatProfile: Boolean): Result<Unit> =
    resealVault("lock", "lock saved") { p ->
        p.put("idle_action", settings.idleAction)
        p.put("idle_timeout_seconds", settings.idleTimeoutSeconds)
        p.put("pin_validity_sec", settings.pinValiditySec)
        p.put("panic_action", settings.panicAction)
        if (clearThreatProfile) p.put("threat_profile", "")
    }


suspend fun MessengerStore.applyThreatPreset(presetId: String): Result<String> {
    val bundle = THREAT_PRESET_BUNDLES[presetId]
        ?: return Result.failure(IllegalArgumentException("unknown preset: $presetId"))
    val result = resealVault("apply-preset", "threat preset applied: $presetId") { p ->
        p.put("threat_profile", presetId)

        p.put("show_login_form", bundle.showLoginForm)
        p.put("say_passphrase_is_default", bundle.sayPassphraseIsDefault)

        p.put("idle_action", bundle.idleAction)
        p.put("idle_timeout_seconds", bundle.idleTimeoutSeconds)
        p.put("pin_validity_sec", bundle.pinValiditySec)
        p.put("panic_action", bundle.panicAction)

        p.put("notify_shell_enabled", bundle.notifyShellEnabled)
        p.put("notify_show_sender", bundle.notifyShowSender)
        p.put("notify_show_body", bundle.notifyShowBody)
        p.put("notify_disguise_enabled", bundle.notifyDisguiseEnabled)
        p.put("notify_noisy", bundle.notifyNoisy)
        p.put("notify_persist_until_open", bundle.notifyPersistUntilOpen)
        p.put("notify_deep_link", bundle.notifyDeepLink)
        p.put("notifications_on_lock", bundle.notificationsOnLock)

        p.put("share_target_enabled", bundle.shareTargetEnabled)
        p.put("show_failed_destroy_attempts", bundle.showFailedDestroyAttempts)
    }
    result.exceptionOrNull()?.let { return Result.failure(it) }

    Logger.d(
        "messenger",
        "preset $presetId vault cells written: show_login_form=${bundle.showLoginForm} " +
            "say_passphrase_is_default=${bundle.sayPassphraseIsDefault} " +
            "idle=${bundle.idleAction}@${bundle.idleTimeoutSeconds} " +
            "pin_validity=${bundle.pinValiditySec} panic=${bundle.panicAction} " +
            "notify_shell=${bundle.notifyShellEnabled} sender=${bundle.notifyShowSender} " +
            "body=${bundle.notifyShowBody} disguise=${bundle.notifyDisguiseEnabled} " +
            "noisy=${bundle.notifyNoisy} persist=${bundle.notifyPersistUntilOpen} " +
            "deep_link=${bundle.notifyDeepLink} on_lock=${bundle.notificationsOnLock} " +
            "share_target=${bundle.shareTargetEnabled} " +
            "failed_destroy=${bundle.showFailedDestroyAttempts}",
    )

    applyPresetBypassSlot(bundle)
    return Result.success(applyPresetRetention(bundle))
}


private fun MessengerStore.applyPresetBypassSlot(bundle: ThreatPresetBundle) {
    if (bundle.bypassSlotEnabled) {
        Logger.d("messenger", "preset bypass slot stays on — key left as the person set it")
        return
    }
    val store = unlockKeysStore ?: return
    val current = store.load()
    if (current.bypassKey.isEmpty()) return
    val cleared = current.copy(bypassKey = "")
    store.save(cleared)
    revealKeysUpdater(cleared)
    Logger.d("messenger", "preset bypass slot turned off — key cleared")
}


const val RETENTION_NOTE_REFUSED = "How long messages are kept: not saved."
const val RETENTION_NOTE_UNKNOWN = "How long messages are kept: try again soon."


private suspend fun MessengerStore.applyPresetRetention(bundle: ThreatPresetBundle): String {
    if (!chatDefaultsKnown()) {
        Logger.w("messenger", "preset retention default not sent — chat defaults not known yet")
        appendStatus(
            "how long messages are kept was not changed — this device does not know the current setting yet",
            level = StatusLevel.WARN,
        )
        return RETENTION_NOTE_UNKNOWN
    }
    val receipts = _health.value.defaultSendReceipts
    Logger.d(
        "messenger",
        "preset retention default sent: ${bundle.defaultRetentionSec}s receipts=$receipts",
    )
    val sent = saveChatDefaults(
        ChatDefaultsSettings(
            retentionSeconds = bundle.defaultRetentionSec,
            sendReceipts = receipts,
        ),
    )
    val failure = sent.exceptionOrNull()
    if (failure != null) {
        Logger.w("messenger", "preset retention default refused: ${failure.message ?: "?"}")
        return RETENTION_NOTE_REFUSED
    }
    val label = io.haoma.calculator.messenger.chat.retentionLevels[
        io.haoma.calculator.messenger.chat.retentionOptionIndex(bundle.defaultRetentionSec),
    ].label
    return "How long messages are kept: $label."
}


fun MessengerStore.loadUnlockKeys(): UnlockKeySettings =
    unlockKeysStore?.load() ?: io.haoma.calculator.core.UnlockKeysStore.DEFAULTS


suspend fun MessengerStore.saveUnlockKeys(settings: UnlockKeySettings): Result<Unit> {
    val store = unlockKeysStore
        ?: return Result.failure(IllegalStateException("unlock-keys store unavailable"))
    return try {
        withContext(Dispatchers.IO) { store.save(settings) }
        revealKeysUpdater(settings)
        appendStatus("unlock keys saved")
        Result.success(Unit)
    } catch (t: Throwable) {
        appendStatus("unlock keys: save failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        Result.failure(t)
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
    val session = vaultSessionProvider()
        ?: return Result.failure(IllegalStateException("vault session unavailable (re-unlock first)"))
    return try {
        withContext(Dispatchers.IO) {
            
            
            store.rekey(oldPattern, newPattern)
            
            
            session.mutateAndReseal("change-pattern-pin-sync") { p ->
                p.put("pin", newPattern)
            }
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
    
    
    val oldBytes = oldPass.toByteArray(Charsets.UTF_8)
    val newBytes = newPass.toByteArray(Charsets.UTF_8)
    return try {
        withContext(Dispatchers.IO) {
            session.changePassphrase(oldBytes, newBytes)
        }
        refreshVaultDerivedState(session)
        appendStatus("passphrase rotated")
        Result.success(Unit)
    } catch (t: Throwable) {
        appendStatus(
            "change-passphrase: ${t.message ?: "?"}",
            level = StatusLevel.WARN,
        )
        Result.failure(t)
    } finally {
        java.util.Arrays.fill(oldBytes, 0)
        java.util.Arrays.fill(newBytes, 0)
    }
}


data class NotificationSettings(
    val shellEnabled: Boolean,
    val showSender: Boolean,
    val showBody: Boolean,
    val onLock: Boolean,
    val disguiseEnabled: Boolean,
    val noisy: Boolean,
    
    
    val persistUntilOpen: Boolean,
    val deepLink: Boolean,
    
    
    val showFailedDestroyAttempts: Boolean,
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
    
    
    val showLoginForm: Boolean,
    val sayPassphraseIsDefault: Boolean,
    val bypassSlotEnabled: Boolean,

    
    val idleAction: String,
    val idleTimeoutSeconds: Int,
    val pinValiditySec: Int,
    val panicAction: String,

    
    val defaultRetentionSec: Long,

    
    val notifyShellEnabled: Boolean,
    val notifyShowSender: Boolean,
    val notifyShowBody: Boolean,
    val notifyDisguiseEnabled: Boolean,
    val notifyNoisy: Boolean,
    val notifyPersistUntilOpen: Boolean,
    val notifyDeepLink: Boolean,
    val notificationsOnLock: Boolean,

    
    val shareTargetEnabled: Boolean,
    val showFailedDestroyAttempts: Boolean,
)


data class AdvancedSettings(
    val urlForceChooser: Boolean,
)


data class UnlockKeySettings(
    val patternKey: String,
    val pinKey: String,
    val bypassKey: String,
)


const val TIER_NOBODY = "nobody"
const val TIER_DOMESTIC = "domestic"
const val TIER_POWER = "power"
const val TIER_FORCE = "force"


private const val RETENTION_FOUR_WEEKS = 28L * 24 * 3600
private const val RETENTION_ONE_WEEK = 7L * 24 * 3600
private const val RETENTION_ONE_DAY = 24L * 3600
private const val RETENTION_ONE_HOUR = 3600L


internal val THREAT_PRESET_BUNDLES: Map<String, ThreatPresetBundle> = mapOf(
    TIER_NOBODY to ThreatPresetBundle(
        showLoginForm = false,
        sayPassphraseIsDefault = false,
        bypassSlotEnabled = true,

        idleAction = "soft-lock",
        idleTimeoutSeconds = 900,
        pinValiditySec = 0,
        panicAction = "safe-lock",

        defaultRetentionSec = RETENTION_FOUR_WEEKS,

        notifyShellEnabled = true,
        notifyShowSender = true,
        notifyShowBody = false,
        notifyDisguiseEnabled = false,
        notifyNoisy = true,
        notifyPersistUntilOpen = true,
        notifyDeepLink = true,
        notificationsOnLock = true,

        shareTargetEnabled = true,
        showFailedDestroyAttempts = true,
    ),
    TIER_DOMESTIC to ThreatPresetBundle(
        showLoginForm = true,
        sayPassphraseIsDefault = true,
        bypassSlotEnabled = true,

        idleAction = "soft-lock",
        idleTimeoutSeconds = 300,
        pinValiditySec = 86400,
        panicAction = "safe-lock",

        defaultRetentionSec = RETENTION_ONE_WEEK,

        notifyShellEnabled = true,
        notifyShowSender = false,
        notifyShowBody = false,
        notifyDisguiseEnabled = true,
        notifyNoisy = false,
        notifyPersistUntilOpen = true,
        notifyDeepLink = false,
        notificationsOnLock = true,

        shareTargetEnabled = false,
        showFailedDestroyAttempts = false,
    ),
    TIER_POWER to ThreatPresetBundle(
        showLoginForm = true,
        sayPassphraseIsDefault = true,
        bypassSlotEnabled = false,

        idleAction = "safe-lock",
        idleTimeoutSeconds = 60,
        pinValiditySec = 300,
        panicAction = "hard-lock",

        defaultRetentionSec = RETENTION_ONE_DAY,

        notifyShellEnabled = true,
        notifyShowSender = false,
        notifyShowBody = false,
        notifyDisguiseEnabled = true,
        notifyNoisy = false,
        notifyPersistUntilOpen = false,
        notifyDeepLink = false,
        notificationsOnLock = false,

        shareTargetEnabled = false,
        showFailedDestroyAttempts = false,
    ),
    TIER_FORCE to ThreatPresetBundle(
        showLoginForm = true,
        sayPassphraseIsDefault = true,
        bypassSlotEnabled = false,

        idleAction = "hard-lock",
        idleTimeoutSeconds = 30,
        pinValiditySec = 0,
        panicAction = "self-destruct",

        defaultRetentionSec = RETENTION_ONE_HOUR,

        notifyShellEnabled = false,
        notifyShowSender = false,
        notifyShowBody = false,
        notifyDisguiseEnabled = true,
        notifyNoisy = false,
        notifyPersistUntilOpen = false,
        notifyDeepLink = false,
        notificationsOnLock = false,

        shareTargetEnabled = false,
        showFailedDestroyAttempts = false,
    ),
)
