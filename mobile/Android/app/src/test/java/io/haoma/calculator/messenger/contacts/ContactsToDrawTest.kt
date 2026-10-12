package io.haoma.calculator.messenger.contacts

import io.haoma.calculator.messenger.PeerEntry
import java.io.File
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test


class ContactsToDrawTest {

    @Test
    fun aLinkedDeviceIsNotDrawnAsAContact() {
        val peers = peersFromCorpus()
        val drawn = contactsToDraw(peers)

        assertTrue(
            "the corpus carries no sibling row, so this test checks nothing",
            peers.any { it.sibling },
        )
        assertEquals(
            "a linked device reached the contacts pane. A sibling is self, never a contact.",
            emptyList<String>(),
            drawn.filter { it.sibling }.map { it.id },
        )
    }

    @Test
    fun everybodyElseIsStillDrawn() {
        val peers = peersFromCorpus()
        val drawn = contactsToDraw(peers)
        assertEquals(
            "filtering siblings dropped somebody who is not one",
            peers.filterNot { it.sibling }.map { it.id },
            drawn.map { it.id },
        )
        assertTrue("the corpus has no people in it, so this checks nothing", drawn.isNotEmpty())
    }

    
    @Test
    fun aDeviceThatOnlyHasASiblingDrawsAnEmptyPane() {
        val onlySibling = peersFromCorpus().filter { it.sibling }
        assertTrue("no sibling in the corpus", onlySibling.isNotEmpty())
        assertEquals(emptyList<PeerEntry>(), contactsToDraw(onlySibling))
    }

    private fun peersFromCorpus(): List<PeerEntry> {
        val file = File(corpusDir(), "peers-listed.json")
        val frame = JSONObject(file.readText())
        val arr = frame.getJSONObject("payload").getJSONArray("peers")
        return (0 until arr.length()).map { PeerEntry.fromJson(arr.getJSONObject(it)) }
    }

    private fun corpusDir(): File {
        var dir: File? = File("").absoluteFile
        while (dir != null) {
            val candidate = File(dir, "app/src/test/resources/wire-corpus")
            if (candidate.isDirectory) return candidate
            dir = dir.parentFile
        }
        throw AssertionError("walked to the filesystem root without finding the wire corpus")
    }
}
