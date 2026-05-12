package io.haoma.calculator.core

import android.app.Activity
import android.content.ComponentName
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import io.haoma.calculator.log.Logger


object OemHelper {

    
    private val intents: List<OemIntent> = listOf(
        OemIntent(
            label = "Huawei/Honor — Protected apps",
            manufacturers = setOf("huawei", "honor"),
            component = ComponentName(
                "com.huawei.systemmanager",
                "com.huawei.systemmanager.optimize.process.ProtectActivity",
            ),
        ),
        OemIntent(
            label = "Huawei/Honor — App launch (manual manage)",
            manufacturers = setOf("huawei", "honor"),
            component = ComponentName(
                "com.huawei.systemmanager",
                "com.huawei.systemmanager.startupmgr.ui.StartupNormalAppListActivity",
            ),
        ),
        OemIntent(
            label = "Xiaomi/Redmi — Autostart",
            manufacturers = setOf("xiaomi", "redmi"),
            component = ComponentName(
                "com.miui.securitycenter",
                "com.miui.permcenter.autostart.AutoStartManagementActivity",
            ),
        ),
        OemIntent(
            label = "Xiaomi/Redmi — Battery (per-app)",
            manufacturers = setOf("xiaomi", "redmi"),
            component = ComponentName(
                "com.miui.powerkeeper",
                "com.miui.powerkeeper.ui.HiddenAppsConfigActivity",
            ),
        ),
        OemIntent(
            label = "Oppo — Startup manager",
            manufacturers = setOf("oppo", "realme"),
            component = ComponentName(
                "com.coloros.safecenter",
                "com.coloros.safecenter.permission.startup.StartupAppListActivity",
            ),
        ),
        OemIntent(
            label = "Vivo — Autostart",
            manufacturers = setOf("vivo"),
            component = ComponentName(
                "com.vivo.permissionmanager",
                "com.vivo.permissionmanager.activity.BgStartUpManagerActivity",
            ),
        ),
        OemIntent(
            label = "Samsung — Battery (deep sleep allowlist)",
            manufacturers = setOf("samsung"),
            component = ComponentName(
                "com.samsung.android.lool",
                "com.samsung.android.sm.ui.battery.BatteryActivity",
            ),
        ),
    )

    
    fun hasRecommendation(): Boolean {
        val mfr = Build.MANUFACTURER.lowercase()
        return intents.any { mfr in it.manufacturers }
    }

    
    fun manufacturerLabel(): String =
        Build.MANUFACTURER.replaceFirstChar { it.titlecase() }

    
    fun tryLaunch(activity: Activity): Boolean {
        val mfr = Build.MANUFACTURER.lowercase()
        for (entry in intents) {
            if (mfr !in entry.manufacturers) continue
            val intent = Intent().apply {
                component = entry.component
                addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            }
            val pm = activity.packageManager
            val resolved = pm.resolveActivity(intent, PackageManager.MATCH_DEFAULT_ONLY) != null
            if (!resolved) {
                Logger.d("oem", "skip ${entry.label} (component not found)")
                continue
            }
            try {
                activity.startActivity(intent)
                Logger.i("oem", "launched ${entry.label}")
                return true
            } catch (t: Throwable) {
                Logger.w("oem", "failed to launch ${entry.label}: ${t.message}")
            }
        }
        Logger.i("oem", "no OEM intent resolvable for manufacturer=$mfr")
        return false
    }

    private data class OemIntent(
        val label: String,
        val manufacturers: Set<String>,
        val component: ComponentName,
    )
}
