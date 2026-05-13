package io.haoma.calculator.core.ipc

import org.json.JSONObject


data class Frame(
    val type: String,
    val id: String? = null,
    val payload: JSONObject? = null,
) {
    
    fun encode(): String {
        val obj = JSONObject()
        obj.put("type", type)
        if (!id.isNullOrEmpty()) obj.put("id", id)
        if (payload != null) obj.put("payload", payload)
        return obj.toString()
    }

    companion object {
        
        fun decode(text: String): Frame {
            val obj = JSONObject(text)
            val type = obj.optString("type", "")
            require(type.isNotEmpty()) { "ipc: frame missing type" }
            val id = obj.optString("id", "").ifEmpty { null }
            val payload = obj.optJSONObject("payload")
            return Frame(type = type, id = id, payload = payload)
        }
    }
}


object FrameType {
    
    
    const val Hello = "hello"
    const val Welcome = "system.welcome"
    const val Error = "system.error"
    const val Ping = "ping"
    const val Pong = "pong"
    const val Subscribe = "subscribe"
    const val Subscribed = "subscribed"

    
    const val SetNick = "set_nick"
    const val Nick = "system.self-nick-changed"

    
    const val SetTorPassword = "set_tor_password"
    const val TorPasswordAccepted = "tor_password_accepted"

    
    const val BackendStatus = "backend_status"
    const val TorInfo = "tor_info"
    const val TorInfoResponse = "tor_info_response"
    const val SystemInfo = "system_info"
    const val SystemInfoResponse = "system_info_response"

    
    const val ListPeers = "list_peers"
    const val PeersListed = "peers_listed"
    const val ListChats = "list_chats"
    const val ChatsListed = "chats_listed"

    
    const val PeerUpdated = "peer.updated"
    const val PeerDeleted = "peer.deleted"
    const val ChatUpdated = "chat.updated"
    const val ChatDeleted = "chat.deleted"
    const val ChatCleared = "chat.cleared"
    const val ChatActivityChanged = "chat.activity-changed"
    const val ChatUnreadChanged = "chat.unread-changed"
    const val PeerLastSeenChanged = "peer.last-seen-changed"
    const val PresenceChanged = "presence_changed"

    
    const val EnsureChat = "ensure_chat"
    const val ChatEnsured = "chat_ensured"

    
    const val SetAlias = "set_alias"
    const val AliasUpdated = "alias_updated"
    const val PeerAction = "peer_action"
    const val PeerActionApplied = "peer_action_applied"
    const val GetPeerFingerprint = "get_peer_fingerprint"
    const val PeerFingerprint = "peer_fingerprint"

    
    const val GetChatSettings = "get_chat_settings"
    const val SetChatSettings = "set_chat_settings"
    const val ChatSettings = "chat.settings-changed"

    
    const val ChatAction = "chat_action"
    const val ChatActionApplied = "chat_action_applied"

    
    const val SendText = "send_text"
    const val TextSent = "text_sent"
    const val SendEdit = "send_edit"
    const val EditSent = "edit_sent"
    const val SendDelete = "send_delete"
    const val DeleteSent = "delete_sent"
    const val SendReaction = "send_reaction"
    const val ReactionSent = "reaction_sent"
    const val TimelineEvent = "msg.timeline-event"
    const val TimelineEventDeleted = "msg.deleted"
    const val DeliveryStatus = "delivery.state-changed"
    const val ListTimeline = "list_timeline"
    const val TimelinePage = "timeline_page"
    const val MarkRead = "mark_read"
    const val MarkedRead = "marked_read"
    const val ClientFocus = "client_focus"

    
    const val PairOnionInvite = "pair_onion_invite"
    const val PairOnionStarted = "pair_onion_started"
    const val PairOnionAccept = "pair_onion_accept"
    const val PairOnionAccepted = "pair_onion_accepted"
    const val PairOnionCancel = "pair_onion_cancel"
    const val PairOnionCancelled = "pair_onion_cancelled"
    const val PairOnionProbe = "pair.onion-probe"
    const val PairOnionCompleted = "pair.onion-completed"
    const val PairOnionFailed = "pair.onion-failed"
    const val PeerPaired = "pair.completed"

    
    const val NewCircuitForPeer = "new_circuit_for_peer"
    const val NewCircuitClosed = "new_circuit_closed"

    
    const val PeerSelfProbe = "peer_self_probe"
    const val PeerSelfProbed = "peer_self_probed"
    const val PeerSelfReachChanged = "peer.self-reach-changed"
    const val ExternalProbeBurst = "external_probe_burst"
    const val ExternalProbeAccepted = "external_probe_accepted"
    const val ExternalReachChanged = "health.external-reach-changed"

    
    const val RotateBegin = "rotate_begin"
    const val RotateBegun = "rotate_begun"
    const val RotateUserAccept = "rotate_user_accept"
    const val RotateUserDecline = "rotate_user_decline"
    const val RotateRequested = "rotate.requested"
    const val RotateLifecycle = "rotate.lifecycle"

    
    const val ClientLockState = "client_lock_state"
    const val NotificationEmitted = "system.notification-emitted"

    
    const val SyncSettings = "sync_settings"

    
    const val StartCall = "start_call"
    const val CallStarted = "call_started"
    const val RespondCall = "respond_call"
    const val CallResponded = "call_responded"
    const val CallStateChanged = "call.state-changed"
    const val CallControl = "call_control"      
    const val CallControlled = "call_controlled" 
    const val CallStreamEvent = "call.stream-event" 

    
    const val SendFile = "send_file"
    const val FileSent = "file_sent"
    const val ListFiles = "list_files"
    const val FilesList = "files_list"
    const val SaveFile = "save_file"
    const val FileSaved = "file_saved"
    const val OpenFile = "open_file"
    const val FileOpenReady = "file_open_ready"
    const val FileProgress = "msg.file-progress"
}
