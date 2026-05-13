package io.haoma.calculator.messenger

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
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
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
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
import io.haoma.calculator.messenger.status.StatusScaffold


@Composable
fun MessengerScaffold(store: MessengerStore) {
    val current by store.current.collectAsStateWithLifecycle()
    val backStack by store.backStack.collectAsStateWithLifecycle()

    BackHandler(enabled = backStack.size > 1) {
        store.popBack()
    }

    
    val connected by store.connection.collectAsStateWithLifecycle()
    LaunchedEffect(connected, current) {
        if (connected) {
            store.requestSelfProbeForActiveSurface()
            store.requestExternalProbeBurst()
        }
    }

    Scaffold(
        modifier = Modifier.fillMaxSize(),
        containerColor = BG_BASE,
        bottomBar = { MessengerBottomBar(current = current, onSelect = store::selectTab, store = store) },
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
private fun MessengerBottomBar(current: Screen, onSelect: (Tab) -> Unit, store: MessengerStore) {
    val activeTab = (current as? Screen.Tabbed)?.tab
    
    
    NavigationBar(
        modifier = Modifier.height(NAV_BAR_HEIGHT),
        containerColor = BG_BAR,
        contentColor = FG_BAR,
    ) {
        ReadinessStrip(store = store)
        NavTabs.forEach { entry ->
            val selected = activeTab == entry.tab
            
            
            NavigationBarItem(
                selected = selected,
                onClick = { onSelect(entry.tab) },
                icon = {
                    Icon(
                        imageVector = entry.icon,
                        contentDescription = entry.label,
                        modifier = Modifier.size(NAV_ICON_SIZE),
                    )
                },
                colors = navItemColors(),
            )
        }
        Spacer(modifier = Modifier.width(NAV_EDGE_INSET))
    }
}

private val NAV_EDGE_INSET = 16.dp
private val NAV_BAR_HEIGHT = 58.dp
private val NAV_ICON_SIZE = 20.dp

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
    StatusScaffold(store = store)
}


private val BG_BASE = Color(0xFF1D2021)
private val BG_BAR = Color(0xFF282828)
private val BG_BAR_SELECTED = Color(0xFF3C3836)
private val FG_BAR = Color(0xFF928374)
private val FG_BAR_SELECTED = Color(0xFFEBDBB2)
private val FG_BAR_DISABLED = Color(0xFF504945)
