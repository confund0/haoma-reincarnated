package io.haoma.calculator.messenger.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CheckboxDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Tab
import androidx.compose.material3.TabRow
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.CtaButton
import io.haoma.calculator.messenger.DangerButton
import io.haoma.calculator.messenger.DestroyWaiting
import io.haoma.calculator.messenger.DeviceLinkDraft
import io.haoma.calculator.messenger.DeviceLinkMode
import io.haoma.calculator.messenger.DeviceLinkPhase
import io.haoma.calculator.messenger.DeviceLinkRun
import io.haoma.calculator.messenger.HaomaPalette
import io.haoma.calculator.messenger.JoinCountdown
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.Section
import io.haoma.calculator.messenger.beginDeviceLinkRun
import io.haoma.calculator.messenger.cancelDeviceLinkRun
import io.haoma.calculator.messenger.destroyLinkedDevice
import io.haoma.calculator.messenger.endDeviceLinkRun
import io.haoma.calculator.messenger.guardOnlyIsOffered
import io.haoma.calculator.messenger.invites.EffShort
import io.haoma.calculator.messenger.invites.QrOverlay
import io.haoma.calculator.messenger.invites.QrScannerVector
import io.haoma.calculator.messenger.invites.rememberQrScannerLauncher
import io.haoma.calculator.messenger.readDestroyGuard
import io.haoma.calculator.messenger.readLinkedDevices
import io.haoma.calculator.messenger.updateDeviceLinkDraft
import io.haoma.calculator.messenger.setDeviceLinkPins
import io.haoma.calculator.messenger.setDeviceLinkWords
import io.haoma.calculator.messenger.splitWords
import io.haoma.calculator.messenger.startDeviceLink
import io.haoma.calculator.messenger.startDeviceLinkFromDraft
import io.haoma.calculator.messenger.startDeviceLinkGuardRetry
import io.haoma.calculator.messenger.stopDestroyWaiting
import io.haoma.calculator.messenger.unlinkLinkedDevice
import io.haoma.calculator.messenger.verifyOtherDestroyPin
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch


@Composable
fun LinkedDevicesSection(store: MessengerStore, onBack: () -> Unit) {
    val run by store.deviceLinkRun.collectAsStateWithLifecycle()
    val devices by store.linkedDevices.collectAsStateWithLifecycle()
    val guard by store.destroyGuard.collectAsStateWithLifecycle()
    val peers by store.peers.collectAsStateWithLifecycle()
    val chats by store.chats.collectAsStateWithLifecycle()

    
    LaunchedEffect(Unit) {
        store.readLinkedDevices()
        store.readDestroyGuard()
    }

    Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
        SectionHeader(title = LINKED_DEVICES_LABEL, store = store, onBack = onBack)
        Column(modifier = Modifier.fillMaxSize().verticalScroll(rememberScrollState())) {
            when (linkedDevicesView(run, devices, guard?.waiting)) {
                LinkedDevicesView.Form -> LinkForm(
                    store = store,
                    joinOffered = joinTabIsOffered(peers.size, chats.size),
                )
                LinkedDevicesView.Run -> RunView(store = store, run = run!!)
                LinkedDevicesView.Waiting -> WaitingView(store = store, waiting = guard!!.waiting!!)
                LinkedDevicesView.Linked -> LinkedView(
                    store = store,
                    card = linkedDeviceCard(devices.first(), guard),
                    peerId = devices.first().peerId,
                    setPinOffered = store.guardOnlyIsOffered(),
                )
            }
            Spacer(modifier = Modifier.height(24.dp))
        }
    }
}


private enum class LinkTab(val label: String) {
    Invite(TAB_INVITE),
    Join(TAB_JOIN),
}

@Composable
private fun LinkForm(store: MessengerStore, joinOffered: Boolean) {
    
    
    val draft by store.deviceLinkDraft.collectAsStateWithLifecycle()
    val tab = if (draft.joinTab) LinkTab.Join else LinkTab.Invite
    val selected = if (joinOffered) tab else LinkTab.Invite

    if (joinOffered) {
        TabRow(
            selectedTabIndex = selected.ordinal,
            containerColor = HaomaPalette.BG_BAR,
            contentColor = HaomaPalette.FG_LINK,
            divider = { HorizontalDivider(color = HaomaPalette.DIVIDER, thickness = 0.5.dp) },
        ) {
            LinkTab.entries.forEach { entry ->
                Tab(
                    selected = selected == entry,
                    onClick = { store.updateDeviceLinkDraft { it.copy(joinTab = entry == LinkTab.Join) } },
                    selectedContentColor = HaomaPalette.FG_PRIMARY,
                    unselectedContentColor = HaomaPalette.FG_DIM,
                ) {
                    Text(
                        text = entry.label,
                        fontSize = 13.sp,
                        fontWeight = FontWeight.SemiBold,
                        modifier = Modifier.padding(vertical = 14.dp),
                    )
                }
            }
        }
    }

    when (selected) {
        LinkTab.Invite -> InviteForm(store, draft)
        LinkTab.Join -> JoinForm(store, draft)
    }

    if (!joinOffered) {
        Section(label = "Joining") {
            Text(
                text = boldedPath(JOIN_NOT_OFFERED),
                color = HaomaPalette.FG_SECONDARY,
                fontSize = 13.sp,
                lineHeight = 18.sp,
            )
        }
    }
}

@Composable
private fun InviteForm(store: MessengerStore, draft: DeviceLinkDraft) {
    Section(label = ACTION_INVITE, description = PIN_HELP) {
        PinFields(
            mine = draft.myPin,
            other = draft.otherPin,
            enabled = true,
            onMine = { v -> store.updateDeviceLinkDraft { d -> d.copy(myPin = v) } },
            onOther = { v -> store.updateDeviceLinkDraft { d -> d.copy(otherPin = v) } },
        )
        Spacer(modifier = Modifier.height(12.dp))
        CtaButton(label = ACTION_INVITE, accent = HaomaPalette.BTN_GIVE) {
            store.startDeviceLinkFromDraft(DeviceLinkMode.Inviting)
        }
    }
}

@Composable
private fun JoinForm(store: MessengerStore, draft: DeviceLinkDraft) {
    val context = LocalContext.current
    
    
    val launchScanner = rememberQrScannerLauncher { decoded ->
        store.updateDeviceLinkDraft { it.copy(words = decoded.trim()) }
    }

    Section(label = ACTION_JOIN, description = PIN_HELP) {
        WordsField(
            words = draft.words,
            enabled = true,
            onWords = { v -> store.updateDeviceLinkDraft { d -> d.copy(words = v) } },
            onScan = launchScanner,
        )
        Spacer(modifier = Modifier.height(12.dp))
        PinFields(
            mine = draft.myPin,
            other = draft.otherPin,
            enabled = true,
            onMine = { v -> store.updateDeviceLinkDraft { d -> d.copy(myPin = v) } },
            onOther = { v -> store.updateDeviceLinkDraft { d -> d.copy(otherPin = v) } },
        )
        Spacer(modifier = Modifier.height(12.dp))
        CtaButton(label = ACTION_JOIN, accent = HaomaPalette.BTN_PRIMARY) {
            val words = splitWords(store.deviceLinkDraft.value.words)
            store.startDeviceLinkFromDraft(DeviceLinkMode.Joining, EffShort.looksValid(context, words))
        }
    }
}


@Composable
private fun RunView(store: MessengerStore, run: DeviceLinkRun) {
    val context = LocalContext.current
    var showQr by remember { mutableStateOf(false) }
    val collecting = run.phase == DeviceLinkPhase.Collecting

    
    Section(label = runTitle(run.mode)) {
        PinFields(
            mine = run.myPin,
            other = run.otherPin,
            enabled = collecting,
            onMine = { store.setDeviceLinkPins(it, run.otherPin) },
            onOther = { store.setDeviceLinkPins(run.myPin, it) },
        )
        Spacer(modifier = Modifier.height(12.dp))

        
        when (wordsSlot(run.mode)) {
            WordsSlot.Readout -> WordsReadout(
                words = run.words.joinToString(" "),
                released = run.wordsReleased,
                onShowQr = { showQr = true },
            )
            WordsSlot.Field -> WordsField(
                words = run.wordsInput,
                enabled = collecting,
                onWords = { store.setDeviceLinkWords(it) },
                onScan = null,
            )
            WordsSlot.None -> Unit
        }

        Spacer(modifier = Modifier.height(16.dp))
        RunLog(run)

        
        if (run.dialStartedAtMs != 0L && run.phase == DeviceLinkPhase.Working) {
            Spacer(modifier = Modifier.height(6.dp))
            JoinCountdown(startedAtMs = run.dialStartedAtMs, link = true)
        }

        if (run.note.isNotEmpty()) {
            Spacer(modifier = Modifier.height(10.dp))
            Text(text = run.note, color = HaomaPalette.C_WARN, fontSize = 13.sp, lineHeight = 18.sp)
        }

        Spacer(modifier = Modifier.height(16.dp))
        
        
        Row(horizontalArrangement = Arrangement.spacedBy(10.dp), verticalAlignment = Alignment.CenterVertically) {
            if (collecting) {
                CtaButton(
                    label = runAction(run.mode),
                    accent = HaomaPalette.BTN_PRIMARY,
                ) {
                    val words = splitWords(run.wordsInput)
                    store.startDeviceLink(run.mode != DeviceLinkMode.Joining || EffShort.looksValid(context, words))
                }
            }
            if (run.retryable) {
                CtaButton(label = ACTION_SET_DESTROY_PIN, accent = HaomaPalette.BTN_GIVE) {
                    store.startDeviceLinkGuardRetry()
                }
            }
            if (run.phase == DeviceLinkPhase.Done) {
                CtaButton(label = ACTION_CLOSE, accent = HaomaPalette.BTN_PRIMARY) {
                    store.endDeviceLinkRun()
                }
            } else {
                CtaButton(label = ACTION_CANCEL, accent = HaomaPalette.BTN_DIM) {
                    store.cancelDeviceLinkRun()
                }
            }
        }
    }

    if (showQr && run.wordsReleased) {
        QrOverlay(words = run.words.joinToString(" "), onDismiss = { showQr = false })
    }
}


@Composable
private fun RunLog(run: DeviceLinkRun) {
    val rows = deviceLinkLogRows(run)
    Column(modifier = Modifier.fillMaxWidth()) {
        rows.forEach { row ->
            Row(
                modifier = Modifier.fillMaxWidth().padding(vertical = 3.dp),
                verticalAlignment = Alignment.Top,
            ) {
                Box(modifier = Modifier.width(22.dp), contentAlignment = Alignment.TopStart) {
                    when {
                        row.spinner -> CircularProgressIndicator(
                            color = HaomaPalette.FG_LINK,
                            strokeWidth = 2.dp,
                            modifier = Modifier.height(13.dp).width(13.dp),
                        )
                        row.marker.isNotEmpty() -> Text(
                            text = row.marker,
                            color = when (row.marker) {
                                LOG_MARKER_DONE -> HaomaPalette.C_OK
                                LOG_MARKER_WARNING -> HaomaPalette.C_WARN
                                else -> HaomaPalette.C_DANGER
                            },
                            fontFamily = FontFamily.Monospace,
                            fontSize = 13.sp,
                        )
                    }
                }
                Text(
                    text = row.text,
                    color = when {
                        row.torSlow -> HaomaPalette.C_ATTENTION
                        row.aside -> HaomaPalette.FG_SECONDARY
                        else -> HaomaPalette.FG_PRIMARY
                    },
                    fontSize = 13.sp,
                    lineHeight = 18.sp,
                    modifier = Modifier.weight(1f),
                )
            }
        }
    }
}


@Composable
private fun LinkedView(
    store: MessengerStore,
    card: LinkedDeviceCard,
    peerId: String,
    setPinOffered: Boolean,
) {
    
    
    val action by store.linkedAction.collectAsStateWithLifecycle()
    val busyLabel = action.busyLabel
    val note = if (action.peerId == peerId) action.note else ""
    var dialog by remember { mutableStateOf<LinkedDialog?>(null) }
    var risksAcked by remember { mutableStateOf(false) }

    Section(label = LINKED_DEVICES_LABEL) {
        Text(
            text = card.idShort,
            color = HaomaPalette.FG_PRIMARY,
            fontFamily = FontFamily.Monospace,
            fontSize = 15.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Spacer(modifier = Modifier.height(4.dp))
        Text(
            text = "Linked ${linkedAtLine(card.linkedAtSeconds)}",
            color = HaomaPalette.FG_DIM,
            fontSize = 13.sp,
        )
        Spacer(modifier = Modifier.height(10.dp))
        Text(
            text = if (card.guardHeld) GUARD_HELD else GUARD_NOT_HELD,
            color = HaomaPalette.FG_SECONDARY,
            fontSize = 13.sp,
            lineHeight = 18.sp,
        )
        Text(
            text = if (card.saltHeld) SALT_HELD else SALT_NOT_HELD,
            color = HaomaPalette.FG_SECONDARY,
            fontSize = 13.sp,
            lineHeight = 18.sp,
        )
        if (busyLabel.isNotEmpty()) {
            Spacer(modifier = Modifier.height(10.dp))
            Row(verticalAlignment = Alignment.CenterVertically) {
                CircularProgressIndicator(
                    color = HaomaPalette.FG_LINK,
                    strokeWidth = 2.dp,
                    modifier = Modifier.height(13.dp).width(13.dp),
                )
                Spacer(modifier = Modifier.width(8.dp))
                Text(
                    text = linkedActionBusyLine(busyLabel),
                    color = HaomaPalette.FG_PRIMARY,
                    fontSize = 13.sp,
                    lineHeight = 18.sp,
                )
            }
        } else if (note.isNotEmpty()) {
            Spacer(modifier = Modifier.height(10.dp))
            Text(text = note, color = HaomaPalette.FG_PRIMARY, fontSize = 13.sp, lineHeight = 18.sp)
        }
    }

    if (setPinOffered) {
        Section(label = ACTION_SET_DESTROY_PIN, description = SET_DESTROY_PIN_HELP) {
            CtaButton(label = ACTION_SET_DESTROY_PIN, accent = HaomaPalette.BTN_GIVE) {
                store.beginDeviceLinkRun(DeviceLinkMode.GuardOnly)
            }
        }
    }

    
    Section(label = "Actions") {
        Row(horizontalArrangement = Arrangement.spacedBy(10.dp), verticalAlignment = Alignment.CenterVertically) {
            CtaButton(
                label = ACTION_VERIFY_DESTROY_PIN,
                accent = HaomaPalette.BTN_PRIMARY,
                
                
                enabled = card.canCheckOtherPin && busyLabel.isEmpty(),
            ) { dialog = LinkedDialog.Verify }
        }
    }

    
    LinkedDangerSection(
        risksAcked = risksAcked,
        
        actionsEnabled = busyLabel.isEmpty(),
        onRiskCheck = { risksAcked = it },
        onUnlink = { dialog = LinkedDialog.Unlink },
        onDestroy = { dialog = LinkedDialog.Destroy },
    )

    when (dialog) {
        LinkedDialog.Verify -> PinActionDialog(
            title = ACTION_VERIFY_DESTROY_PIN,
            body = "This checks your other device's destroy PIN. Nothing is changed either way.",
            confirm = "Check",
            onDismiss = { dialog = null },
            onConfirm = { pin ->
                dialog = null
                store.scope.launch { store.verifyOtherDestroyPin(peerId, pin) }
            },
        )

        LinkedDialog.Destroy -> PinActionDialog(
            title = ACTION_DESTROY,
            body = destroyConfirmText(peerId),
            confirm = ACTION_DESTROY,
            onDismiss = { dialog = null },
            onConfirm = { pin ->
                dialog = null
                store.scope.launch { store.destroyLinkedDevice(peerId, pin) }
            },
        )

        LinkedDialog.Unlink -> ConfirmDialog(
            title = ACTION_UNLINK,
            body = unlinkConfirmText(peerId),
            confirm = ACTION_UNLINK,
            onDismiss = { dialog = null },
            onConfirm = {
                dialog = null
                store.scope.launch { store.unlinkLinkedDevice(peerId) }
            },
        )

        null -> Unit
    }
}


@Composable
private fun WaitingView(store: MessengerStore, waiting: DestroyWaiting) {
    var acked by remember { mutableStateOf(false) }
    var note by remember { mutableStateOf("") }
    val scope = rememberCoroutineScope()

    
    var nowSeconds by remember { mutableStateOf(System.currentTimeMillis() / 1000L) }
    LaunchedEffect(waiting.deadline) {
        while (true) {
            delay(30_000L)
            nowSeconds = System.currentTimeMillis() / 1000L
        }
    }

    Section(label = WAITING_TITLE) {
        Text(
            text = destroyWaitText(destroyWaitRemaining(waiting.deadline, nowSeconds)),
            color = HaomaPalette.FG_SECONDARY,
            fontSize = 14.sp,
            lineHeight = 20.sp,
        )
        Spacer(modifier = Modifier.height(14.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Checkbox(
                checked = acked,
                onCheckedChange = { acked = it },
                colors = CheckboxDefaults.colors(
                    checkedColor = HaomaPalette.C_ATTENTION,
                    uncheckedColor = HaomaPalette.FG_DIM,
                    checkmarkColor = HaomaPalette.BG_BASE,
                ),
            )
            Spacer(modifier = Modifier.width(8.dp))
            Text(text = WAITING_ACK, color = HaomaPalette.FG_PRIMARY, fontSize = 14.sp)
        }
        Spacer(modifier = Modifier.height(12.dp))
        CtaButton(
            label = ACTION_STOP_WAITING,
            accent = HaomaPalette.BTN_GIVE,
            enabled = acked,
        ) {
            note = ""
            scope.launch { note = store.stopDestroyWaiting().text }
        }
        if (note.isNotEmpty()) {
            Spacer(modifier = Modifier.height(12.dp))
            Text(text = note, color = HaomaPalette.FG_PRIMARY, fontSize = 13.sp, lineHeight = 18.sp)
        }
    }
}


@Composable
private fun LinkedDangerSection(
    risksAcked: Boolean,
    actionsEnabled: Boolean,
    onRiskCheck: (Boolean) -> Unit,
    onUnlink: () -> Unit,
    onDestroy: () -> Unit,
) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 16.dp),
    ) {
        Text(
            text = "DANGER",
            color = HaomaPalette.C_DANGER,
            fontSize = 13.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Spacer(modifier = Modifier.height(10.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Checkbox(
                checked = risksAcked,
                onCheckedChange = onRiskCheck,
                colors = CheckboxDefaults.colors(
                    checkedColor = HaomaPalette.C_DANGER,
                    uncheckedColor = HaomaPalette.FG_DIM,
                    checkmarkColor = HaomaPalette.BG_BASE,
                ),
            )
            Spacer(modifier = Modifier.width(8.dp))
            Text(text = "I understand risks", color = HaomaPalette.FG_PRIMARY, fontSize = 14.sp)
        }
        Spacer(modifier = Modifier.height(12.dp))
        DangerButton(
            label = ACTION_UNLINK,
            enabled = risksAcked && actionsEnabled,
            modifier = Modifier.fillMaxWidth(),
            onClick = onUnlink,
        )
        Spacer(modifier = Modifier.height(8.dp))
        DangerButton(
            label = ACTION_DESTROY,
            enabled = risksAcked && actionsEnabled,
            modifier = Modifier.fillMaxWidth(),
            onClick = onDestroy,
        )
    }
    HorizontalDivider(color = HaomaPalette.DIVIDER, thickness = 0.5.dp)
}

private enum class LinkedDialog { Verify, Unlink, Destroy }

private fun linkedAtLine(seconds: Long): String {
    if (seconds <= 0L) return "at an unknown time"
    val days = (System.currentTimeMillis() / 1000L - seconds) / 86_400L
    return when {
        days <= 0L -> "today"
        days == 1L -> "yesterday"
        else -> "$days days ago"
    }
}


@Composable
private fun PinFields(
    mine: String,
    other: String,
    enabled: Boolean,
    onMine: (String) -> Unit,
    onOther: (String) -> Unit,
) {
    PinField(value = mine, label = FIELD_MY_PIN, enabled = enabled, onChange = onMine)
    Spacer(modifier = Modifier.height(10.dp))
    PinField(value = other, label = FIELD_OTHER_PIN, enabled = enabled, onChange = onOther)
}

@Composable
private fun PinField(value: String, label: String, enabled: Boolean, onChange: (String) -> Unit) {
    OutlinedTextField(
        value = value,
        onValueChange = { onChange(it.filter { c -> c.isDigit() }.take(4)) },
        enabled = enabled,
        singleLine = true,
        visualTransformation = PasswordVisualTransformation(),
        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.NumberPassword, imeAction = ImeAction.Next),
        label = { Text(text = label, color = HaomaPalette.FG_DIM, fontSize = 13.sp) },
        colors = dialogFieldColors(),
        shape = RoundedCornerShape(10.dp),
        modifier = Modifier.fillMaxWidth(),
    )
}

@Composable
private fun WordsField(
    words: String,
    enabled: Boolean,
    onWords: (String) -> Unit,
    onScan: (() -> Unit)?,
) {
    OutlinedTextField(
        value = words,
        onValueChange = onWords,
        enabled = enabled,
        leadingIcon = if (onScan == null) {
            null
        } else {
            {
                IconButton(onClick = onScan) {
                    Icon(
                        imageVector = QrScannerVector,
                        contentDescription = "Scan QR",
                        tint = HaomaPalette.FG_LINK,
                    )
                }
            }
        },
        label = { Text(text = FIELD_WORDS, color = HaomaPalette.FG_DIM, fontSize = 13.sp) },
        textStyle = TextStyle(
            color = HaomaPalette.FG_PRIMARY,
            fontSize = 14.sp,
            fontFamily = FontFamily.Monospace,
        ),
        colors = dialogFieldColors(),
        shape = RoundedCornerShape(10.dp),
        minLines = 1,
        maxLines = 3,
        modifier = Modifier.fillMaxWidth().heightIn(min = 44.dp),
    )
}


@Composable
private fun WordsReadout(words: String, released: Boolean, onShowQr: () -> Unit) {
    Box(
        modifier = Modifier
            .fillMaxWidth()
            .background(HaomaPalette.BG_BANNER)
            .padding(horizontal = 10.dp, vertical = 10.dp),
    ) {
        Text(
            text = if (released) words else "The words appear here once the offer is live.",
            color = if (released) HaomaPalette.C_WARN else HaomaPalette.FG_DIM,
            fontFamily = FontFamily.Monospace,
            fontSize = 14.sp,
            fontWeight = FontWeight.SemiBold,
        )
    }
    if (released) {
        Spacer(modifier = Modifier.height(8.dp))
        Text(
            text = "Show QR",
            color = HaomaPalette.BTN_PRIMARY,
            fontSize = 13.sp,
            fontWeight = FontWeight.SemiBold,
            modifier = Modifier
                .clickable(onClick = onShowQr)
                .padding(vertical = 4.dp),
        )
    }
}


@Composable
private fun ConfirmDialog(
    title: String,
    body: String,
    confirm: String,
    onDismiss: () -> Unit,
    onConfirm: () -> Unit,
) {
    LinkedDialogFrame(
        title = title,
        onDismiss = onDismiss,
        confirm = confirm,
        confirmEnabled = true,
        onConfirm = onConfirm,
    ) {
        Text(text = body, color = HaomaPalette.FG_SECONDARY, fontSize = 13.sp, lineHeight = 18.sp)
    }
}

@Composable
private fun PinActionDialog(
    title: String,
    body: String,
    confirm: String,
    onDismiss: () -> Unit,
    onConfirm: (String) -> Unit,
) {
    var pin by remember { mutableStateOf("") }
    LinkedDialogFrame(
        title = title,
        onDismiss = onDismiss,
        confirm = confirm,
        confirmEnabled = pin.length == 4,
        onConfirm = { onConfirm(pin) },
    ) {
        Text(text = body, color = HaomaPalette.FG_SECONDARY, fontSize = 13.sp, lineHeight = 18.sp)
        Spacer(modifier = Modifier.height(10.dp))
        PinField(
            value = pin,
            label = FIELD_OTHER_PIN,
            enabled = true,
            onChange = { pin = it },
        )
    }
}

@Composable
private fun LinkedDialogFrame(
    title: String,
    onDismiss: () -> Unit,
    confirm: String,
    confirmEnabled: Boolean,
    onConfirm: () -> Unit,
    body: @Composable () -> Unit,
) {
    MaterialTheme(
        colorScheme = darkColorScheme(
            surface = HaomaPalette.BG_BAR,
            onSurface = HaomaPalette.FG_PRIMARY,
            background = HaomaPalette.BG_BAR,
            onBackground = HaomaPalette.FG_PRIMARY,
        ),
    ) {
        AlertDialog(
            onDismissRequest = onDismiss,
            title = { Text(title, color = HaomaPalette.FG_PRIMARY) },
            text = { Column { body() } },
            confirmButton = {
                TextButton(enabled = confirmEnabled, onClick = onConfirm) {
                    Text(
                        text = confirm,
                        color = if (confirmEnabled) HaomaPalette.C_DANGER else HaomaPalette.FG_DIM,
                        fontWeight = FontWeight.SemiBold,
                    )
                }
            },
            dismissButton = {
                TextButton(onClick = onDismiss) {
                    Text(text = ACTION_CANCEL, color = HaomaPalette.FG_LINK)
                }
            },
            containerColor = HaomaPalette.BG_BAR,
        )
    }
}
