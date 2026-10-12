package io.haoma.calculator.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test


class VaultHelperCommitResultTest {
    private val unsealed = VaultHelper.Unsealed(ByteArray(0), IdlePolicy.Default, ByteArray(0))

    @Test
    fun exitZeroIsSwapped() {
        assertTrue(VaultHelper.commitResultFor(0) { unsealed } is VaultHelper.CommitResult.Swapped)
    }

    @Test
    fun exitOneIsWrongPassphrase() {
        assertEquals(VaultHelper.CommitResult.WrongPassphrase, VaultHelper.commitResultFor(1) { error("not read") })
    }

    @Test
    fun exitThreeIsNotApplied() {
        assertEquals(VaultHelper.CommitResult.NotApplied, VaultHelper.commitResultFor(3) { error("not read") })
    }

    @Test
    fun exitFiveIsStoreInUse() {
        assertEquals(VaultHelper.CommitResult.StoreInUse, VaultHelper.commitResultFor(5) { error("not read") })
    }

    @Test
    fun exitSixIsSwappedWithoutBundle() {
        
        assertEquals(VaultHelper.CommitResult.SwappedNoBundle, VaultHelper.commitResultFor(6) { error("not read") })
    }

    @Test
    fun exitSevenIsPending() {
        assertEquals(VaultHelper.CommitResult.Pending, VaultHelper.commitResultFor(7) { error("not read") })
    }

    @Test
    fun exitFourIsBroken() {
        assertEquals(VaultHelper.CommitResult.Broken, VaultHelper.commitResultFor(4) { error("not read") })
    }

    @Test
    fun exitEightIsNotEmpty() {
        
        assertEquals(VaultHelper.CommitResult.NotEmpty, VaultHelper.commitResultFor(8) { error("not read") })
    }

    @Test
    fun theRestoreEntryOpensThePickerOnlyOnExitZero() {
        assertEquals(VaultHelper.RestoreEntry.Allowed, VaultHelper.restoreEntryFor(0))
        assertEquals(VaultHelper.RestoreEntry.NotEmpty, VaultHelper.restoreEntryFor(8))
        assertEquals(VaultHelper.RestoreEntry.Pending, VaultHelper.restoreEntryFor(7))
    }
}
