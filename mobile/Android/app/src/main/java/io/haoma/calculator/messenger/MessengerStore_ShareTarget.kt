package io.haoma.calculator.messenger

import android.content.ComponentName
import android.content.pm.PackageManager
import io.haoma.calculator.core.VaultSession
import io.haoma.calculator.log.Logger


private const val SHARE_ALIAS = "io.haoma.calculator.ShareReceiverActivity"


internal fun MessengerStore.reconcileShareTarget(session: VaultSession) {
    val ctx = appContext ?: return
    val snap = session.snapshot()
    val optedIn = snap.optBoolean("share_target_enabled", false)
    val noThreat = snap.optString("threat_profile", "").isEmpty()
    val enable = optedIn && noThreat
    val newState = if (enable) {
        PackageManager.COMPONENT_ENABLED_STATE_ENABLED
    } else {
        PackageManager.COMPONENT_ENABLED_STATE_DISABLED
    }
    try {
        val cn = ComponentName(ctx, SHARE_ALIAS)
        val pm = ctx.packageManager
        if (pm.getComponentEnabledSetting(cn) != newState) {
            pm.setComponentEnabledSetting(cn, newState, PackageManager.DONT_KILL_APP)
            Logger.d(
                "messenger",
                "share-target alias enabled=$enable (optedIn=$optedIn noThreat=$noThreat)",
            )
        }
    } catch (t: Throwable) {
        Logger.w("messenger", "share-target reconcile failed: ${t.message}")
    }
}


fun MessengerStore.loadShareTargetEnabled(): Boolean {
    val session = vaultSessionProvider() ?: return false
    return session.snapshot().optBoolean("share_target_enabled", false)
}


suspend fun MessengerStore.setShareTargetEnabled(enabled: Boolean): Result<Unit> =
    resealVault(
        "share-target",
        "share target ${if (enabled) "enabled" else "disabled"}",
    ) { p ->
        p.put("share_target_enabled", enabled)
    }
