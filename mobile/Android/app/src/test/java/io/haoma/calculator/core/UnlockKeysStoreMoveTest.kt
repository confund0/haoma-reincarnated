package io.haoma.calculator.core

import java.io.File
import java.nio.file.Files
import java.nio.file.attribute.PosixFilePermissions
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder


class UnlockKeysStoreMoveTest {
    @get:Rule
    val tmp = TemporaryFolder()

    private fun legacy(body: String? = "OLD-KEYS"): File {
        val f = File(tmp.root, UnlockKeysStore.DEFAULT_FILE_NAME)
        if (body != null) f.writeText(body)
        return f
    }

    private fun target(): File = File(File(tmp.root, "haoma"), UnlockKeysStore.DEFAULT_FILE_NAME)

    @Test
    fun movesTheOldFileIntoTheDataDir() {
        val old = legacy()
        val dataDir = File(tmp.root, "haoma").apply { mkdirs() }
        val new = File(dataDir, UnlockKeysStore.DEFAULT_FILE_NAME)

        assertEquals(UnlockKeysStore.LegacyMove.Moved, UnlockKeysStore.moveLegacyFile(old, new))
        assertFalse(old.exists())
        assertEquals("OLD-KEYS", new.readText())
    }

    @Test
    fun keepsTheDataDirFileWhenBothExist() {
        val old = legacy()
        val new = target().apply { parentFile!!.mkdirs(); writeText("NEW-KEYS") }

        assertEquals(UnlockKeysStore.LegacyMove.OldDropped, UnlockKeysStore.moveLegacyFile(old, new))
        assertFalse(old.exists())
        assertEquals("NEW-KEYS", new.readText())
    }

    @Test
    fun doesNothingWithoutAnOldFile() {
        val old = legacy(body = null)
        val new = target()

        assertEquals(UnlockKeysStore.LegacyMove.Nothing, UnlockKeysStore.moveLegacyFile(old, new))
        assertFalse(new.exists())
        assertFalse(new.parentFile!!.exists())
    }

    @Test
    fun aDataDirMadeForTheMoveIsOwnerOnly() {
        val old = legacy()
        val new = target()

        assertEquals(UnlockKeysStore.LegacyMove.Moved, UnlockKeysStore.moveLegacyFile(old, new))
        assertTrue(new.exists())
        
        assertEquals(
            "rwx------",
            PosixFilePermissions.toString(Files.getPosixFilePermissions(new.parentFile!!.toPath())),
        )
    }
}
