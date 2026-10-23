package io.haoma.calculator.messenger.settings

import org.junit.Assert.assertEquals
import org.junit.Test


class AdvancedTabTest {
    @Test
    fun `tab order is privacy, backup, logs, erase`() {
        assertEquals(
            listOf(AdvancedTab.Privacy, AdvancedTab.Backup, AdvancedTab.Logs, AdvancedTab.Erase),
            AdvancedTab.entries,
        )
    }
}
