package io.haoma.calculator.messenger.settings


internal object SettingsDomains {
    const val Profile = "profile"
    const val Appearance = "appearance"
    const val Defaults = "defaults"
    const val Files = "files"
    const val Lock = "lock"
    const val Tor = "tor"
    const val Notifications = "notifications"
    const val LinkedDevices = "linked-devices"
    const val Advanced = "advanced"

    val Order: List<String> = listOf(
        Profile,
        Defaults,
        Notifications,
        Lock,
        Appearance,
        Files,
        Tor,
        LinkedDevices,
        Advanced,
    )

    
    val Labels: Map<String, String> = mapOf(
        Profile to "Profile",
        Appearance to "Appearance",
        Defaults to "Chat defaults",
        Files to "Files",
        Lock to "Security",
        Tor to "Tor",
        Notifications to "Notifications",
        LinkedDevices to LINKED_DEVICES_LABEL,
        Advanced to "Advanced",
    )

    
    val Hints: Map<String, String> = mapOf(
        Profile to "Self nick",
        Appearance to "Chat font size",
        Defaults to "Disappearing messages, read receipts",
        Files to "Handled by the system file picker",
        Lock to "Idle action, panic action, PIN, passphrase",
        Tor to "Control-port password",
        Notifications to "Per-OS banners, sender + body privacy",
        LinkedDevices to "Your other device, destroy PIN, unlink",
        Advanced to "Privacy, backup, logs, erase",
    )
}
