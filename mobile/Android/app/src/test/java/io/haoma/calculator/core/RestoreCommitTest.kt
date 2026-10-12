package io.haoma.calculator.core

import java.io.File
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder


class RestoreCommitTest {
    @get:Rule
    val tmp = TemporaryFolder()

    
    private class Calls(vararg answers: VaultHelper.CommitResult, val stopOk: Boolean = true) {
        val log = mutableListOf<String>()
        private val left = answers.toMutableList()
        val commit: () -> VaultHelper.CommitResult = {
            log += "commit"
            left.removeAt(0)
        }
        val stop: suspend () -> Boolean = {
            log += "stop"
            stopOk
        }
    }

    private val swapped = VaultHelper.CommitResult.Swapped(
        VaultHelper.Unsealed(ByteArray(0), IdlePolicy.Default, ByteArray(0)),
    )

    @Test
    fun aStoreInUseStopsTheServiceBeforeTheSecondCommit() = runTest {
        val c = Calls(VaultHelper.CommitResult.StoreInUse, swapped)
        val got = commitStoppingDaemons(c.commit, c.stop)
        assertEquals(listOf("commit", "stop", "commit"), c.log)
        assertEquals(RestoreAttempt.Done(swapped), got)
    }

    @Test
    fun aStopThatTimesOutCommitsNothingMore() = runTest {
        val c = Calls(VaultHelper.CommitResult.StoreInUse, swapped, stopOk = false)
        val got = commitStoppingDaemons(c.commit, c.stop)
        assertEquals(listOf("commit", "stop"), c.log)
        assertEquals(RestoreAttempt.StopFailed, got)
    }

    @Test
    fun aWrongPassphraseStopsNothing() = runTest {
        val c = Calls(VaultHelper.CommitResult.WrongPassphrase)
        val got = commitStoppingDaemons(c.commit, c.stop)
        assertEquals(listOf("commit"), c.log)
        assertEquals(RestoreAttempt.Done(VaultHelper.CommitResult.WrongPassphrase), got)
    }

    @Test
    fun aSwapStopsNothing() = runTest {
        val c = Calls(swapped)
        commitStoppingDaemons(c.commit, c.stop)
        assertEquals(listOf("commit"), c.log)
    }

    @Test
    fun aMarkerSaysReinstallAndNamesNoDir() {
        val marker = File(tmp.root, VaultHelper.RESTORE_MARKER)
        marker.writeText("""{"staging":".staging-20261001-115900"}""")
        val msg = VaultHelper.pendingRestoreMessage(marker)
        assertEquals(
            "The restore did not finish, so this device cannot be opened. " +
                "Uninstall the app, install it again, then restore your backup.",
            msg,
        )
        assertEquals(UnlockManager.RESTORE_BROKEN, msg)
        assertEquals(UnlockManager.RESTORE_PENDING, msg)
    }

    @Test
    fun noMarkerNoMessage() {
        assertNull(VaultHelper.pendingRestoreMessage(File(tmp.root, VaultHelper.RESTORE_MARKER)))
    }

    @Test
    fun anUnreadableMarkerStillBlocks() {
        val marker = File(tmp.root, VaultHelper.RESTORE_MARKER).apply { writeText("not json") }
        assertEquals(VaultHelper.RESTORE_DID_NOT_FINISH, VaultHelper.pendingRestoreMessage(marker))
    }

    @Test
    fun theNotEmptySentenceMatchesHaomaVault() {
        
        assertEquals(
            "This device holds contacts or chats. A backup can only be restored on an empty device. " +
                "Unlock the app and wipe it, or reinstall the app, then restore. " +
                "If anything on this device is valuable, make a backup of it first.",
            UnlockManager.RESTORE_NOT_EMPTY,
        )
    }
}
