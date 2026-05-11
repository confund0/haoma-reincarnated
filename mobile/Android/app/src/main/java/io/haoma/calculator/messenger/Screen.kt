package io.haoma.calculator.messenger


enum class Tab {
    Chats,
    Contacts,
    Invites,
    Settings,
    Status,
}


sealed interface Screen {
    
    data class Tabbed(val tab: Tab) : Screen

    
    data class ChatDetail(val chatId: String) : Screen

    
    data class ContactDetail(val peerId: String) : Screen

    
    data class ChatSettings(val chatId: String) : Screen

    
    data class SettingsSection(val domain: String) : Screen

    
    data class Accept(val type: PairType) : Screen
}


enum class PairType(val label: String) {
    Tor("Tor"),
    QR("QR"),
    DHT("DHT"),
}
