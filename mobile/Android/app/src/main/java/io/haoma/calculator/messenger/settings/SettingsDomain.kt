package io.haoma.calculator.messenger.settings


internal object SettingsDomains {
    const val Profile = "profile"
    const val Defaults = "defaults"
    const val Files = "files"
    const val Lock = "lock"
    const val Tor = "tor"
    const val Notifications = "notifications"
    const val Advanced = "advanced"

    val Order: List<String> = listOf(
        Profile,
        Defaults,
        Files,
        Lock,
        Tor,
        Notifications,
        Advanced,
    )

    val Labels: Map<String, String> = mapOf(
        Profile to "Profile",
        Defaults to "Chat defaults",
        Files to "Files",
        Lock to "Lock",
        Tor to "Tor",
        Notifications to "Notifications",
        Advanced to "Advanced",
    )

    
    val Hints: Map<String, String> = mapOf(
        Profile to "Self nick",
        Defaults to "Disappearing messages, read receipts",
        Files to "Handled by the system file picker",
        Lock to "Idle action, panic action, PIN, passphrase",
        Tor to "Control-port password",
        Notifications to "Per-OS banners, sender + body privacy",
        Advanced to "Security warnings",
    )
}
