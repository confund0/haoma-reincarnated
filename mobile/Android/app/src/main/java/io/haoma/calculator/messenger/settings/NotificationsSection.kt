package io.haoma.calculator.messenger.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.core.app.NotificationManagerCompat
import io.haoma.calculator.messenger.DirtyBar
import io.haoma.calculator.messenger.HaomaPalette
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.NotificationSettings
import io.haoma.calculator.messenger.Section
import io.haoma.calculator.messenger.loadNotificationSettings
import io.haoma.calculator.messenger.saveNotificationSettings
import kotlinx.coroutines.launch


@Composable
internal fun NotificationsSection(store: MessengerStore, onBack: () -> Unit) {
    var initial by remember { mutableStateOf(store.loadNotificationSettings()) }
    val coroutineScope = rememberCoroutineScope()
    val scrollState = rememberScrollState()

    val snapshot = initial
    if (snapshot == null) {
        Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
            SectionHeader(title = "Notifications", store = store, onBack = onBack)
            VaultUnavailableBanner(message = "Vault session unavailable — re-unlock the app to edit notification settings.")
        }
        return
    }

    var shellEnabled by remember(snapshot) { mutableStateOf(snapshot.shellEnabled) }
    var showSender by remember(snapshot) { mutableStateOf(snapshot.showSender) }
    var showBody by remember(snapshot) { mutableStateOf(snapshot.showBody) }
    var onLock by remember(snapshot) { mutableStateOf(snapshot.onLock) }
    var disguiseEnabled by remember(snapshot) { mutableStateOf(snapshot.disguiseEnabled) }
    var noisy by remember(snapshot) { mutableStateOf(snapshot.noisy) }
    var saving by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }

    val current by remember(shellEnabled, showSender, showBody, onLock, disguiseEnabled, noisy) {
        derivedStateOf {
            NotificationSettings(
                shellEnabled = shellEnabled,
                showSender = showSender,
                showBody = showBody,
                onLock = onLock,
                disguiseEnabled = disguiseEnabled,
                noisy = noisy,
            )
        }
    }
    val dirty by remember(current, snapshot) { derivedStateOf { current != snapshot } }

    val ctx = LocalContext.current
    val permGranted = remember { NotificationManagerCompat.from(ctx).areNotificationsEnabled() }

    Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
        SectionHeader(title = "Notifications", store = store, onBack = onBack)
        DirtyBar(
            visible = dirty && !saving,
            onTap = { coroutineScope.launch { scrollState.animateScrollTo(scrollState.maxValue) } },
        )

        Column(modifier = Modifier.weight(1f).verticalScroll(scrollState)) {
            Section(label = "Banner posture") {
                ToggleRow(
                    label = "Enable per-OS notifications",
                    hint = "Shows banners via the OS notification channel.",
                    checked = shellEnabled,
                    onCheckedChange = { shellEnabled = it; if (error != null) error = null },
                )
                ToggleRow(
                    label = "Show sender name in notifications",
                    hint = "Off = banners hide who sent the message.",
                    checked = showSender,
                    onCheckedChange = { showSender = it; if (error != null) error = null },
                )
                ToggleRow(
                    label = "Show message body in notifications",
                    hint = "Off = banners hide the message text.",
                    checked = showBody,
                    onCheckedChange = { showBody = it; if (error != null) error = null },
                )
                ToggleRow(
                    label = "Allow notifications while UI is locked",
                    hint = "Off = soft-locked sessions stay silent.",
                    checked = onLock,
                    onCheckedChange = { onLock = it; if (error != null) error = null },
                )
                ToggleRow(
                    label = "Noisy notifications",
                    hint = "Standard messenger posture: lock-screen visible + heads-up banners while unlocked. Off = silent tray entry, lock-screen hidden.",
                    checked = noisy,
                    onCheckedChange = { noisy = it; if (error != null) error = null },
                )
            }

            Section(label = "Disguise mode") {
                ToggleRow(
                    label = "Disguise notifications as cover-skin tips",
                    hint = "Only when both Show toggles above are off. Banners look like calculator math tips; tapping opens a tip page in the calculator.",
                    checked = disguiseEnabled,
                    onCheckedChange = { disguiseEnabled = it; if (error != null) error = null },
                )
            }

            if (!permGranted) {
                Section(label = "OS permission") {
                    Text(
                        text = "Android has not granted notification permission. Toggles below persist, but banners won't appear until you re-enable in system Settings → Apps → Calculator → Notifications.",
                        color = HaomaPalette.C_DANGER,
                        fontSize = 13.sp,
                        lineHeight = 18.sp,
                    )
                }
            }

            Section(
                label = "Privacy posture",
                description = "With both Show toggles off, banners read \"Haoma: New message\" — safest under physical inspection.",
            ) {}

            Spacer(modifier = Modifier.height(8.dp))

            SaveResetRow(
                dirty = dirty,
                saving = saving,
                onSave = {
                    val saveSnapshot = current
                    saving = true
                    error = null
                    coroutineScope.launch {
                        val result = store.saveNotificationSettings(saveSnapshot)
                        saving = false
                        result.onSuccess { initial = store.loadNotificationSettings() }
                        result.onFailure { t -> error = t.message ?: "save failed" }
                    }
                },
                onReset = {
                    shellEnabled = snapshot.shellEnabled
                    showSender = snapshot.showSender
                    showBody = snapshot.showBody
                    onLock = snapshot.onLock
                    disguiseEnabled = snapshot.disguiseEnabled
                    noisy = snapshot.noisy
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

            Spacer(modifier = Modifier.height(24.dp))
        }
    }
}
