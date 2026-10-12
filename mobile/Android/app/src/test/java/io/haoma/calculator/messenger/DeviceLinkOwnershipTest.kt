package io.haoma.calculator.messenger

import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test


class DeviceLinkOwnershipTest {

    
    @Test
    fun noComposableStartsTheCeremonyOnItsOwnScope() {
        val offenders = mutableListOf<String>()
        for (file in composableSources()) {
            val text = file.readText()
            for (banned in BANNED_FROM_COMPOSABLES) {
                if (text.contains("$banned(")) {
                    offenders += "${file.name} calls $banned"
                }
            }
        }
        assertEquals(
            "A screen must not start the device-linking ceremony on a scope it owns. " +
                "The first step moves the run to Working, which redraws the screen and " +
                "destroys that scope, cancelling the request mid-handshake. " +
                "Call startDeviceLink() or startDeviceLinkGuardRetry() instead — they " +
                "launch on the store's scope. Offenders: $offenders",
            emptyList<String>(),
            offenders,
        )
    }

    
    @Test
    fun theScanReachesTheDeviceLinkingScreen() {
        val names = composableSources().map { it.name }
        assertTrue(
            "the source scan found no files, so it checked nothing: $names",
            names.isNotEmpty(),
        )
        assertTrue(
            "the source scan did not reach the device-linking screen, so the guard " +
                "would pass with the fault present: $names",
            names.contains("LinkedDevicesSection.kt"),
        )
    }

    private fun composableSources(): List<File> {
        val root = moduleRoot()
        val main = File(root, "app/src/main/java/io/haoma/calculator")
        return main.walkTopDown()
            .filter { it.isFile && it.extension == "kt" }
            .filter { it.readText().contains("@Composable") }
            .toList()
    }

    
    private fun moduleRoot(): File {
        var dir: File? = File("").absoluteFile
        while (dir != null) {
            if (File(dir, "app/build.gradle.kts").isFile) return dir
            dir = dir.parentFile
        }
        throw AssertionError("walked to the filesystem root without finding the Android module")
    }

    private companion object {
        val BANNED_FROM_COMPOSABLES = listOf("proceedDeviceLink", "retryDeviceLinkGuard")
    }
}
