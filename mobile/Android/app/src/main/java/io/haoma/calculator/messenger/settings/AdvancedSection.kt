package io.haoma.calculator.messenger.settings

import android.widget.Toast
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.HaomaApp
import io.haoma.calculator.core.HaomaCoreService
import io.haoma.calculator.core.VaultHelper
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.AdvancedSettings
import io.haoma.calculator.messenger.CtaButton
import io.haoma.calculator.messenger.DirtyBar
import io.haoma.calculator.messenger.HaomaPalette
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.Section
import io.haoma.calculator.messenger.loadAdvancedSettings
import io.haoma.calculator.messenger.loadSecurityWarnings
import io.haoma.calculator.messenger.saveAdvancedSettings
import io.haoma.calculator.saf.SafBridge
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeoutOrNull


@Composable
internal fun AdvancedSection(store: MessengerStore, onBack: () -> Unit) {
    var initial by remember { mutableStateOf(store.loadAdvancedSettings()) }
    val warnings = remember { store.loadSecurityWarnings() }
    val coroutineScope = rememberCoroutineScope()
    val scrollState = rememberScrollState()

    val snapshot = initial
    if (snapshot == null) {
        Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
            SectionHeader(title = "Advanced", store = store, onBack = onBack)
            VaultUnavailableBanner(message = "Vault session unavailable — re-unlock the app to edit Advanced settings.")
        }
        return
    }

    var urlForceChooser by remember(snapshot) { mutableStateOf(snapshot.urlForceChooser) }
    var saving by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var backingUp by remember { mutableStateOf(false) }

    val context = LocalContext.current
    val backupLauncher = rememberLauncherForActivityResult(
        contract = remember { ActivityResultContracts.CreateDocument(BACKUP_MIME) },
    ) { uri ->
        if (uri == null) {
            Logger.i("backup", "SAF picker cancelled")
            return@rememberLauncherForActivityResult
        }
        val app = context.applicationContext as? HaomaApp ?: return@rememberLauncherForActivityResult
        val appCtx = context.applicationContext
        backingUp = true
        coroutineScope.launch {
            val outcome = withContext(Dispatchers.IO) {
                runCatching {
                    
                    
                    val signal = HaomaCoreService.stopAndAwait(appCtx)
                    withTimeoutOrNull(BACKUP_STOP_TIMEOUT_MS) { signal.await() }
                    
                    
                    delay(BACKUP_WRITEBACK_SETTLE_MS)
                    val cacheFile = File(appCtx.cacheDir, defaultBackupName())
                    try {
                        VaultHelper.archiveWrite(appCtx, cacheFile.absolutePath)
                        SafBridge.copyDaemonOutputToUri(appCtx, cacheFile.absolutePath, uri)
                    } finally {
                        if (cacheFile.exists() && !cacheFile.delete()) {
                            Logger.w("backup", "cache slot survived deletion: ${cacheFile.absolutePath}")
                        }
                    }
                }
            }
            outcome.onSuccess { bytes ->
                Logger.i("backup", "ok bytes=$bytes — hard-locking")
                Toast.makeText(appCtx, "Backup complete — app locked.", Toast.LENGTH_SHORT).show()
            }.onFailure { t ->
                Logger.e("backup", "failed", t)
                Toast.makeText(appCtx, "Backup failed: ${t.message ?: "?"}", Toast.LENGTH_LONG).show()
            }
            
            
            app.idleLockDispatcher.forceHardLock("backup")
            backingUp = false
        }
    }

    val current by remember(urlForceChooser) {
        derivedStateOf { AdvancedSettings(urlForceChooser = urlForceChooser) }
    }
    val dirty by remember(current, snapshot) { derivedStateOf { current != snapshot } }

    Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
        SectionHeader(title = "Advanced", store = store, onBack = onBack)
        DirtyBar(
            visible = dirty && !saving,
            onTap = { coroutineScope.launch { scrollState.animateScrollTo(scrollState.maxValue) } },
        )

        Column(modifier = Modifier.weight(1f).verticalScroll(scrollState)) {
            Section(label = "Links") {
                ToggleRow(
                    label = "Always choose how to open links",
                    hint = "Show app picker instead of using your default browser.",
                    checked = urlForceChooser,
                    onCheckedChange = { urlForceChooser = it; if (error != null) error = null },
                )
            }

            Spacer(modifier = Modifier.height(4.dp))

            SaveResetRow(
                dirty = dirty,
                saving = saving,
                onSave = {
                    val saveSnapshot = current
                    saving = true
                    error = null
                    coroutineScope.launch {
                        val result = store.saveAdvancedSettings(saveSnapshot)
                        saving = false
                        result.onSuccess { initial = store.loadAdvancedSettings() }
                        result.onFailure { t -> error = t.message ?: "save failed" }
                    }
                },
                onReset = {
                    urlForceChooser = snapshot.urlForceChooser
                    error = null
                },
            )

            error?.let { message ->
                Spacer(modifier = Modifier.height(10.dp))
                Text(
                    text = message,
                    color = HaomaPalette.C_DANGER,
                    fontSize = 13.sp,
                    modifier = Modifier.padding(horizontal = 16.dp),
                )
            }

            Spacer(modifier = Modifier.height(12.dp))

            Section(label = "Security warnings") {
                when {
                    warnings == null -> Text(
                        text = "Vault session unavailable — re-unlock the app to see security warnings.",
                        color = HaomaPalette.FG_SECONDARY,
                        fontSize = 13.sp,
                    )

                    warnings.isEmpty() -> Text(
                        text = "None.",
                        color = HaomaPalette.FG_SECONDARY,
                        fontSize = 14.sp,
                    )

                    else -> Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
                        warnings.forEach { w ->
                            Text(
                                text = "• $w",
                                color = HaomaPalette.FG_PRIMARY,
                                fontSize = 14.sp,
                            )
                        }
                    }
                }
            }

            Section(
                label = "About",
                description = "Warnings are emitted when a tunable (PIN validity, idle timeout, etc.) sits outside its recommended range. The producer that fills this list lands with the Security Health screen — until then it stays empty.",
            ) {}

            Section(
                label = "Backup",
                description = "Saves an encrypted archive of every chat, contact, attachment, and setting. The archive is sealed with your master passphrase — pick any cloud, SD card, or USB-OTG drive. The app locks itself when the backup finishes; unlock again to keep using it.",
            ) {
                CtaButton(
                    label = "Back up to file…",
                    accent = HaomaPalette.BTN_PRIMARY,
                    enabled = !backingUp,
                ) {
                    backupLauncher.launch(defaultBackupName())
                }
            }

            Spacer(modifier = Modifier.height(24.dp))
        }
    }

    if (backingUp) {
        BackupOverlay()
    }
}

@Composable
private fun BackupOverlay() {
    Box(
        modifier = Modifier
            .fillMaxSize()
            .background(HaomaPalette.BG_BASE.copy(alpha = 0.85f)),
        contentAlignment = Alignment.Center,
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            CircularProgressIndicator(
                color = HaomaPalette.BTN_PRIMARY,
                strokeWidth = 2.dp,
                modifier = Modifier.height(20.dp).width(20.dp),
            )
            Spacer(modifier = Modifier.width(12.dp))
            Text(
                text = "Backing up…",
                color = HaomaPalette.FG_PRIMARY,
                fontSize = 14.sp,
            )
        }
    }
}

private const val BACKUP_MIME = "application/octet-stream"


private const val BACKUP_STOP_TIMEOUT_MS = 8000L


private const val BACKUP_WRITEBACK_SETTLE_MS = 250L

private fun defaultBackupName(): String {
    val stamp = SimpleDateFormat("yyyyMMdd-HHmmss", Locale.US).format(Date())
    return "haoma-backup-$stamp.tar.zst"
}
