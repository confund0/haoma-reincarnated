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
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.clickable
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Tab
import androidx.compose.material3.TabRow
import androidx.compose.material3.Text
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.PopupProperties
import io.haoma.calculator.HaomaApp
import io.haoma.calculator.core.HaomaCoreService
import io.haoma.calculator.core.VaultHelper
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.AdvancedSettings
import io.haoma.calculator.messenger.CtaButton
import io.haoma.calculator.messenger.HaomaDropdownItem
import io.haoma.calculator.messenger.HaomaPalette
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.Section
import io.haoma.calculator.messenger.loadAdvancedSettings
import io.haoma.calculator.messenger.openAdvancedLogs
import io.haoma.calculator.messenger.saveAdvancedSettings
import io.haoma.calculator.messenger.status.LogsSection
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
    var selectedTab by remember { mutableStateOf(store.advancedSelectedTab) }

    Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
        SectionHeader(title = "Advanced", store = store, onBack = onBack)
        AdvancedTabRow(
            selected = selectedTab,
            onSelect = {
                selectedTab = it
                store.advancedSelectedTab = it
            },
        )
        Box(modifier = Modifier.weight(1f).fillMaxWidth()) {
            when (selectedTab) {
                AdvancedTab.Privacy -> PrivacyTab(store)
                AdvancedTab.Backup -> BackupTab(store)
                AdvancedTab.Logs -> LogsTab(store)
                AdvancedTab.Erase -> EraseTab(store)
            }
        }
    }
}


internal enum class AdvancedTab(val label: String) {
    Privacy("Privacy"),
    Backup("Backup"),
    Logs("Logs"),
    Erase("Erase"),
}

@Composable
private fun AdvancedTabRow(selected: AdvancedTab, onSelect: (AdvancedTab) -> Unit) {
    TabRow(
        selectedTabIndex = selected.ordinal,
        containerColor = HaomaPalette.BG_BAR,
        contentColor = HaomaPalette.FG_LINK,
        divider = { HorizontalDivider(color = HaomaPalette.DIVIDER, thickness = 0.5.dp) },
    ) {
        AdvancedTab.entries.forEach { tab ->
            Tab(
                selected = selected == tab,
                onClick = { onSelect(tab) },
                selectedContentColor = HaomaPalette.FG_PRIMARY,
                unselectedContentColor = HaomaPalette.FG_DIM,
            ) {
                Text(
                    text = tab.label,
                    fontSize = 13.sp,
                    fontWeight = FontWeight.SemiBold,
                    modifier = Modifier.padding(vertical = 14.dp),
                )
            }
        }
    }
}


@Composable
private fun PrivacyTab(store: MessengerStore) {
    var initial by remember { mutableStateOf(store.loadAdvancedSettings()) }
    val coroutineScope = rememberCoroutineScope()

    val snapshot = initial
    if (snapshot == null) {
        VaultUnavailableBanner(message = "Vault session unavailable — re-unlock the app to edit Advanced settings.")
        return
    }

    var urlForceChooser by remember(snapshot) { mutableStateOf(snapshot.urlForceChooser) }
    var saving by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }

    Column(modifier = Modifier.fillMaxSize().verticalScroll(rememberScrollState())) {
        Section(label = "Links") {
            ToggleRow(
                label = "Always choose how to open links",
                hint = "Show app picker instead of using your default browser.",
                checked = urlForceChooser,
                enabled = !saving,
                onCheckedChange = { newValue ->
                    val previous = urlForceChooser
                    urlForceChooser = newValue
                    error = null
                    saving = true
                    coroutineScope.launch {
                        val result = store.saveAdvancedSettings(snapshot.copy(urlForceChooser = newValue))
                        saving = false
                        result.onSuccess { initial = store.loadAdvancedSettings() }
                        result.onFailure { t ->
                            
                            
                            urlForceChooser = previous
                            error = t.message ?: "save failed"
                        }
                    }
                },
            )
            if (saving) {
                Spacer(modifier = Modifier.height(8.dp))
                Row(verticalAlignment = Alignment.CenterVertically) {
                    CircularProgressIndicator(
                        color = HaomaPalette.BTN_PRIMARY,
                        strokeWidth = 2.dp,
                        modifier = Modifier.height(16.dp).width(16.dp),
                    )
                    Spacer(modifier = Modifier.width(8.dp))
                    Text(text = "Saving…", color = HaomaPalette.FG_DIM, fontSize = 12.sp)
                }
            }
        }

        error?.let { message ->
            Text(
                text = message,
                color = HaomaPalette.C_DANGER,
                fontSize = 13.sp,
                modifier = Modifier.padding(horizontal = 16.dp),
            )
        }

        Spacer(modifier = Modifier.height(24.dp))
    }
}


@Composable
private fun BackupTab(store: MessengerStore) {
    val coroutineScope = rememberCoroutineScope()
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

    Column(modifier = Modifier.fillMaxSize().verticalScroll(rememberScrollState())) {
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


private val LOG_LEVEL_LABELS: List<String> = listOf(
    "Default (build setting)",
    "error",
    "warn",
    "info",
    "debug",
)
private val LOG_LEVEL_VALUES: List<String> = listOf("", "error", "warn", "info", "debug")

private fun logLevelIndexOf(value: String): Int {
    val idx = LOG_LEVEL_VALUES.indexOf(value)
    return if (idx >= 0) idx else 0
}

@Composable
private fun LogsTab(store: MessengerStore) {
    var initial by remember { mutableStateOf(store.loadAdvancedSettings()) }
    val coroutineScope = rememberCoroutineScope()

    val snapshot = initial
    if (snapshot == null) {
        VaultUnavailableBanner(message = "Vault session unavailable — re-unlock the app to edit Advanced settings.")
        return
    }

    var levelIndex by remember(snapshot) { mutableStateOf(logLevelIndexOf(snapshot.logLevel)) }
    var callStats by remember(snapshot) { mutableStateOf(snapshot.callStats) }
    var saving by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }

    Column(modifier = Modifier.fillMaxSize().verticalScroll(rememberScrollState())) {
        Section(label = "Logs") {
            Text(
                text = "Open logs",
                color = HaomaPalette.FG_LINK,
                fontSize = 14.sp,
                fontWeight = FontWeight.SemiBold,
                modifier = Modifier
                    .fillMaxWidth()
                    .clickable { store.openAdvancedLogs() }
                    .padding(vertical = 4.dp),
            )
        }

        Section(label = "Level") {
            LogLevelDropdown(
                currentIndex = levelIndex,
                enabled = !saving,
                onPick = { picked ->
                    val previous = levelIndex
                    levelIndex = picked
                    error = null
                    saving = true
                    coroutineScope.launch {
                        val result = store.saveAdvancedSettings(
                            snapshot.copy(logLevel = LOG_LEVEL_VALUES[picked]),
                        )
                        saving = false
                        result.onSuccess { initial = store.loadAdvancedSettings() }
                        result.onFailure { t ->
                            levelIndex = previous
                            error = t.message ?: "save failed"
                        }
                    }
                },
            )
            Spacer(modifier = Modifier.height(8.dp))
            ToggleRow(
                label = "Call stats (every 1 s)",
                hint = "Logs a call-quality line once a second while a call is open.",
                checked = callStats,
                enabled = !saving,
                onCheckedChange = { newValue ->
                    val previous = callStats
                    callStats = newValue
                    error = null
                    saving = true
                    coroutineScope.launch {
                        val result = store.saveAdvancedSettings(snapshot.copy(callStats = newValue))
                        saving = false
                        result.onSuccess { initial = store.loadAdvancedSettings() }
                        result.onFailure { t ->
                            callStats = previous
                            error = t.message ?: "save failed"
                        }
                    }
                },
            )
            if (saving) {
                Spacer(modifier = Modifier.height(8.dp))
                Row(verticalAlignment = Alignment.CenterVertically) {
                    CircularProgressIndicator(
                        color = HaomaPalette.BTN_PRIMARY,
                        strokeWidth = 2.dp,
                        modifier = Modifier.height(16.dp).width(16.dp),
                    )
                    Spacer(modifier = Modifier.width(8.dp))
                    Text(text = "Saving…", color = HaomaPalette.FG_DIM, fontSize = 12.sp)
                }
            }
        }

        error?.let { message ->
            Text(
                text = message,
                color = HaomaPalette.C_DANGER,
                fontSize = 13.sp,
                modifier = Modifier.padding(horizontal = 16.dp),
            )
        }

        Spacer(modifier = Modifier.height(24.dp))
    }
}

@Composable
private fun LogLevelDropdown(currentIndex: Int, enabled: Boolean, onPick: (Int) -> Unit) {
    var expanded by remember { mutableStateOf(false) }
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Text(
            text = "Log level",
            color = HaomaPalette.FG_LINK,
            fontSize = 14.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Box {
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .clickable(enabled = enabled) { expanded = true }
                    .padding(vertical = 4.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(
                    text = LOG_LEVEL_LABELS[currentIndex],
                    color = HaomaPalette.BTN_GIVE,
                    fontFamily = FontFamily.Monospace,
                    fontSize = 14.sp,
                    fontWeight = FontWeight.SemiBold,
                    modifier = Modifier.weight(1f),
                )
                Text(text = "▾", color = HaomaPalette.FG_LINK, fontSize = 14.sp)
            }
            MaterialTheme(
                colorScheme = darkColorScheme(
                    surface = HaomaPalette.BG_BAR,
                    onSurface = HaomaPalette.FG_PRIMARY,
                ),
            ) {
                DropdownMenu(
                    expanded = expanded,
                    onDismissRequest = { expanded = false },
                    properties = PopupProperties(focusable = false),
                ) {
                    LOG_LEVEL_LABELS.forEachIndexed { idx, label ->
                        HaomaDropdownItem(
                            label = label,
                            selected = idx == currentIndex,
                            onClick = { onPick(idx); expanded = false },
                        )
                    }
                }
            }
        }
    }
}


@Composable
internal fun AdvancedLogsScreen(store: MessengerStore, onBack: () -> Unit) {
    Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
        SectionHeader(title = "Logs", store = store, onBack = onBack)
        Box(modifier = Modifier.weight(1f).fillMaxWidth()) {
            LogsSection()
        }
    }
}


@Composable
private fun EraseTab(store: MessengerStore) {
    Column(modifier = Modifier.fillMaxSize().verticalScroll(rememberScrollState())) {
        WipeDeviceBlock(store = store)
        Spacer(modifier = Modifier.height(24.dp))
    }
}

private const val BACKUP_MIME = "application/octet-stream"


private const val BACKUP_STOP_TIMEOUT_MS = 8000L


private const val BACKUP_WRITEBACK_SETTLE_MS = 250L

private fun defaultBackupName(): String {
    val stamp = SimpleDateFormat("yyyyMMdd-HHmmss", Locale.US).format(Date())
    return "haoma-backup-$stamp.tar.zst"
}
