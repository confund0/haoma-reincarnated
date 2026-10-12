package io.haoma.calculator.messenger.contacts

import io.haoma.calculator.messenger.PeerEntry
import java.io.File
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test


class UnreachableWarningTest {

    
    private val planWords =
        "This contact does not answer. If you know they are gone, use Delete peer at the " +
            "bottom of this page. THIS ALSO DELETES THE CHAT HISTORY."

    @Test
    fun aMarkedPairingOpensWithTheWarning() {
        val marked = peersFromCorpus().filter { it.unreachable }
        assertTrue("the corpus carries no marked pairing, so this test checks nothing", marked.isNotEmpty())
        for (p in marked) {
            assertEquals("the details page of ${p.id}", planWords, detailWarningFor(p))
        }
    }

    @Test
    fun anUnmarkedPairingHasNoWarning() {
        val unmarked = peersFromCorpus().filterNot { it.unreachable }
        assertTrue("the corpus carries no unmarked pairing, so this test checks nothing", unmarked.isNotEmpty())
        for (p in unmarked) {
            assertNull("the details page of ${p.id} carries the warning", detailWarningFor(p))
        }
    }

    @Test
    fun aRetiredPairingNeverHasTheWarning() {
        val marked = peersFromCorpus().first { it.unreachable }
        assertNull(detailWarningFor(marked.copy(retiredAt = 1754900000L)))
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
