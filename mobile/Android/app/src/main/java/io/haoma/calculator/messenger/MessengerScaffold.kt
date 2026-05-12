package io.haoma.calculator.messenger

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Email
import androidx.compose.material.icons.filled.Info
import androidx.compose.material.icons.filled.Person
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.Icon
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.NavigationBarItemColors
import androidx.compose.material3.NavigationBarItemDefaults
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.chat.ChatDetailScreen
import io.haoma.calculator.messenger.chat.ChatSettingsScreen
import io.haoma.calculator.messenger.chats.ChatsTab
import io.haoma.calculator.messenger.contacts.ContactDetailScreen
import io.haoma.calculator.messenger.contacts.ContactsTab
import io.haoma.calculator.messenger.invites.AcceptScreen
import io.haoma.calculator.messenger.invites.InvitesTab
import io.haoma.calculator.messenger.settings.SettingsSectionScreen
import io.haoma.calculator.messenger.settings.SettingsTab


@Composable
fun MessengerScaffold(store: MessengerStore) {
    val current by store.current.collectAsStateWithLifecycle()
    val backStack by store.backStack.collectAsStateWithLifecycle()

    BackHandler(enabled = backStack.size > 1) {
        store.popBack()
    }

    Scaffold(
        modifier = Modifier.fillMaxSize(),
        containerColor = BG_BASE,
        bottomBar = { MessengerBottomBar(current = current, onSelect = store::selectTab) },
    ) { padding ->
        Box(modifier = Modifier.padding(padding).fillMaxSize()) {
            when (val screen = current) {
                is Screen.Tabbed -> TabContent(tab = screen.tab, store = store)
                is Screen.ChatDetail -> ChatDetailScreen(
                    store = store,
                    chatId = screen.chatId,
                    onBack = { store.popBack() },
                )
                is Screen.ContactDetail -> ContactDetailScreen(
                    store = store,
                    peerId = screen.peerId,
                    onBack = { store.popBack() },
                )
                is Screen.ChatSettings -> ChatSettingsScreen(
                    store = store,
                    chatId = screen.chatId,
                    onBack = { store.popBack() },
                )
                is Screen.SettingsSection -> SettingsSectionScreen(
                    store = store,
                    domain = screen.domain,
                    onBack = { store.popBack() },
                )
                is Screen.Accept -> AcceptScreen(
                    store = store,
                    type = screen.type,
                    onBack = { store.popBack() },
                )
            }
            
            
            RingerDialogHost(store = store)
        }
    }
}

@Composable
private fun MessengerBottomBar(current: Screen, onSelect: (Tab) -> Unit) {
    val activeTab = (current as? Screen.Tabbed)?.tab
    
    
    NavigationBar(
        containerColor = BG_BAR,
        contentColor = FG_BAR,
    ) {
        Spacer(modifier = Modifier.width(NAV_EDGE_INSET))
        NavTabs.forEach { entry ->
            val selected = activeTab == entry.tab
            
            
            NavigationBarItem(
                selected = selected,
                onClick = { onSelect(entry.tab) },
                icon = {
                    Icon(
                        imageVector = entry.icon,
                        contentDescription = entry.label,
                    )
                },
                colors = navItemColors(),
            )
        }
        Spacer(modifier = Modifier.width(NAV_EDGE_INSET))
    }
}

private val NAV_EDGE_INSET = 16.dp

private data class NavTabEntry(val tab: Tab, val label: String, val icon: ImageVector)

private val NavTabs = listOf(
    NavTabEntry(Tab.Chats, "Chats", Icons.Filled.Email),
    NavTabEntry(Tab.Contacts, "Contacts", Icons.Filled.Person),
    NavTabEntry(Tab.Invites, "Invites", Icons.Filled.Add),
    NavTabEntry(Tab.Settings, "Settings", Icons.Filled.Settings),
    NavTabEntry(Tab.Status, "Status", Icons.Filled.Info),
)

@Composable
private fun navItemColors(): NavigationBarItemColors = NavigationBarItemDefaults.colors(
    selectedIconColor = FG_BAR_SELECTED,
    selectedTextColor = FG_BAR_SELECTED,
    unselectedIconColor = FG_BAR,
    unselectedTextColor = FG_BAR,
    disabledIconColor = FG_BAR_DISABLED,
    disabledTextColor = FG_BAR_DISABLED,
    indicatorColor = BG_BAR_SELECTED,
)


@Composable
private fun TabContent(tab: Tab, store: MessengerStore) {
    when (tab) {
        Tab.Chats -> ChatsTab(store)
        Tab.Contacts -> ContactsTab(store)
        Tab.Invites -> InvitesTab(store)
        Tab.Settings -> SettingsTab(store)
        Tab.Status -> StatusTab(store)
    }
}


@Composable
private fun StatusTab(store: MessengerStore) {
    val health by store.health.collectAsStateWithLifecycle()
    val log by store.statusLog.collectAsStateWithLifecycle()
    val connection by store.connection.collectAsStateWithLifecycle()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(BG_BASE)
            .padding(horizontal = 12.dp, vertical = 8.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        SystemBar(connection = connection, health = health, store = store)
        Box(modifier = Modifier.weight(1f).fillMaxWidth()) {
            StatusLogList(log = log)
        }
        CommandInput(store = store)
    }
}


@Composable
private fun CommandInput(store: MessengerStore) {
    var input by remember { mutableStateOf("") }
    Surface(
        modifier = Modifier
            .fillMaxWidth()
            .heightIn(min = CommandInputMinHeight),
        shape = RoundedCornerShape(percent = 50),
        color = BG_BAR,
        
        
        border = BorderStroke(1.dp, BORDER_CMD),
    ) {
        BasicTextField(
            value = input,
            onValueChange = { input = it },
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 14.dp, vertical = 8.dp),
            singleLine = true,
            textStyle = TextStyle(
                color = FG_LOG,
                fontFamily = FontFamily.Monospace,
                fontSize = 13.sp,
            ),
            cursorBrush = SolidColor(BORDER_CMD),
            keyboardOptions = KeyboardOptions(imeAction = ImeAction.Send),
            keyboardActions = KeyboardActions(
                onSend = {
                    store.runCommand(input)
                    input = ""
                },
            ),
            decorationBox = { inner ->
                if (input.isEmpty()) {
                    Text(
                        text = "/help",
                        color = FG_DIM,
                        fontFamily = FontFamily.Monospace,
                        fontSize = 13.sp,
                    )
                }
                inner()
            },
        )
    }
}

@Composable
private fun SystemBar(connection: Boolean, health: SystemHealth, store: MessengerStore) {
    val ipcChip = if (connection) "ipc:up" else "ipc:dn"
    val ipcColor = if (connection) C_OK else C_BAD
    val beChip = if (health.backendReachable) "be:up" else "be:dn"
    val beColor = if (health.backendReachable) C_OK else C_BAD
    val torChip = when {
        health.tor.unreachable -> "tor:dn"
        !health.tor.ready -> "tor:${health.tor.bootstrap}%"
        health.onionCount == 0 -> "tor:0/0"
        else -> "tor:${health.onionCount}"
    }
    val torColor = when {
        health.tor.unreachable -> C_BAD
        !health.tor.ready -> C_WARN
        health.onionCount == 0 -> C_WARN
        else -> C_OK
    }
    val nickChip = if (health.selfNick.isEmpty()) "[me: …]" else "[me: ${health.selfNick}]"

    Row(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Chip(text = ipcChip, color = ipcColor)
        Chip(text = beChip, color = beColor)
        Chip(text = torChip, color = torColor)
        Text(
            text = nickChip,
            color = if (health.selfNickIsDefault) C_WARN else FG_BAR_SELECTED,
            fontFamily = FontFamily.Monospace,
            fontSize = 13.sp,
            modifier = Modifier.weight(1f),
        )
        
        
        io.haoma.calculator.messenger.calls.CallChip(store = store)
    }
}

@Composable
private fun Chip(text: String, color: Color) {
    Box(
        modifier = Modifier
            .background(color = color.copy(alpha = 0.18f))
            .padding(horizontal = 6.dp, vertical = 2.dp),
    ) {
        Text(
            text = text,
            color = color,
            fontFamily = FontFamily.Monospace,
            fontSize = 13.sp,
            fontWeight = FontWeight.SemiBold,
        )
    }
}

@Composable
private fun StatusLogList(log: List<StatusLine>) {
    val listState = rememberLazyListState()
    LaunchedEffect(log.size) {
        if (log.isNotEmpty()) listState.scrollToItem(log.size - 1)
    }
    if (log.isEmpty()) {
        Text(
            text = "(log empty — waiting for the first daemon event…)",
            color = FG_DIM,
            fontFamily = FontFamily.Monospace,
            fontSize = 13.sp,
        )
        return
    }
    LazyColumn(
        state = listState,
        modifier = Modifier.fillMaxSize(),
        contentPadding = PaddingValues(vertical = 4.dp),
    ) {
        items(log, key = { it.at.toString() + it.text }) { line ->
            val color = when (line.level) {
                StatusLevel.WARN -> C_WARN
                else -> FG_LOG
            }
            Row(modifier = Modifier.fillMaxWidth().padding(vertical = 1.dp)) {
                Text(
                    text = line.stamp(),
                    color = FG_DIM,
                    fontFamily = FontFamily.Monospace,
                    fontSize = 12.sp,
                )
                Text(
                    text = "  " + line.text,
                    color = color,
                    fontFamily = FontFamily.Monospace,
                    fontSize = 12.sp,
                )
            }
        }
    }
}

@Composable
private fun EmptyTabSurface(title: String, body: String) {
    Box(
        modifier = Modifier
            .fillMaxSize()
            .background(BG_BASE)
            .padding(24.dp),
        contentAlignment = Alignment.Center,
    ) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Text(
                text = title,
                color = FG_BAR_SELECTED,
                fontWeight = FontWeight.SemiBold,
                fontSize = 22.sp,
            )
            Text(
                text = body,
                color = FG_DIM,
                fontSize = 14.sp,
            )
        }
    }
}


private val BG_BASE = Color(0xFF1D2021)
private val BG_BAR = Color(0xFF282828)
private val BG_BAR_SELECTED = Color(0xFF3C3836)
private val FG_BAR = Color(0xFF928374)
private val FG_BAR_SELECTED = Color(0xFFEBDBB2)
private val FG_BAR_DISABLED = Color(0xFF504945)
private val FG_LOG = Color(0xFFD5C4A1)
private val FG_DIM = Color(0xFF7C6F64)
private val C_OK = Color(0xFF5FCC1A)
private val C_WARN = Color(0xFFFABD2F)
private val C_BAD = Color(0xFFCC241D)
private val BORDER_CMD = Color(0xFF83A598) 
private val CommandInputMinHeight = 40.dp
