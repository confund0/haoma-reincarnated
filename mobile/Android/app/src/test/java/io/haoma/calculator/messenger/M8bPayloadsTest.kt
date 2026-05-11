package io.haoma.calculator.messenger

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Test


class M8bPayloadsTest {
    @Test fun setAliasRequestEncodesPeerIdAndAlias() {
        val req = SetAliasRequest(peerId = "peer-1234", alias = "Alice").toJson()
        assertEquals("peer-1234", req.getString("peer_id"))
        assertEquals("Alice", req.getString("alias"))
    }

    @Test fun setAliasRequestEmptyAliasStillEncodes() {
        
        
        val req = SetAliasRequest(peerId = "p", alias = "").toJson()
        assertEquals("", req.getString("alias"))
    }

    @Test fun aliasUpdatedResponseDecodesPeer() {
        val o = JSONObject(
            """{"peer":{"id":"p","alias":"Renamed","label":"Renamed"}}""",
        )
        val resp = AliasUpdatedResponse.fromJson(o)
        assertEquals("p", resp.peer.id)
        assertEquals("Renamed", resp.peer.alias)
        assertEquals("Renamed", resp.peer.label)
    }

    @Test fun peerActionRequestEncodesAction() {
        val retire = PeerActionRequest("p", PeerAction.Retire).toJson()
        assertEquals("retire", retire.getString("action"))
        val delete = PeerActionRequest("p", PeerAction.Delete).toJson()
        assertEquals("delete", delete.getString("action"))
    }

    @Test fun peerActionAppliedDecodesAllFields() {
        val o = JSONObject(
            """{"peer":{"id":"p","label":"Bob"},"action":"delete","deleted_count":42}""",
        )
        val resp = PeerActionAppliedResponse.fromJson(o)
        assertEquals("p", resp.peer.id)
        assertEquals("Bob", resp.peer.label)
        assertEquals("delete", resp.action)
        assertEquals(42, resp.deletedCount)
    }

    @Test fun peerActionAppliedRetireOmitsDeletedCount() {
        
        
        val o = JSONObject("""{"peer":{"id":"p"},"action":"retire"}""")
        val resp = PeerActionAppliedResponse.fromJson(o)
        assertEquals("retire", resp.action)
        assertEquals(0, resp.deletedCount)
    }

    @Test fun getPeerFingerprintRequestEncodesPeerId() {
        val req = GetPeerFingerprintRequest("peer-xyz").toJson()
        assertEquals("peer-xyz", req.getString("peer_id"))
    }

    @Test fun peerFingerprintPayloadDecodesHex() {
        val hex = "a".repeat(66)
        val o = JSONObject("""{"peer_id":"p","fingerprint":"$hex"}""")
        val resp = PeerFingerprintPayload.fromJson(o)
        assertEquals("p", resp.peerId)
        assertEquals(hex, resp.fingerprint)
    }

    @Test fun peerFingerprintPayloadEmptyHexMeansNoSession() {
        
        
        val o = JSONObject("""{"peer_id":"p","fingerprint":""}""")
        val resp = PeerFingerprintPayload.fromJson(o)
        assertEquals("", resp.fingerprint)
    }

    @Test fun ensureChatRequestEncodesPeerId() {
        val req = EnsureChatRequest("peer-42").toJson()
        assertEquals("peer-42", req.getString("peer_id"))
    }

    @Test fun chatEnsuredResponseDecodesBothEntities() {
        val o = JSONObject(
            """{"peer":{"id":"p","chat_id":"c"},"chat":{"chat_id":"c","kind":"direct","peer_id":"p"}}""",
        )
        val resp = ChatEnsuredResponse.fromJson(o)
        assertEquals("p", resp.peer.id)
        assertEquals("c", resp.peer.chatId)
        assertEquals("c", resp.chat.chatId)
        assertEquals(ChatKind.Direct, resp.chat.kind)
    }
}
