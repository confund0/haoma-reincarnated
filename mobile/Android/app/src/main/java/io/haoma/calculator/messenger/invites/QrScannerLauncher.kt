package io.haoma.calculator.messenger.invites

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions


@Composable
fun rememberQrScannerLauncher(onResult: (String) -> Unit): () -> Unit {
    val currentOnResult by rememberUpdatedState(onResult)
    val launcher = rememberLauncherForActivityResult(ScanContract()) { result ->
        val contents = result?.contents
        if (!contents.isNullOrEmpty()) currentOnResult(contents)
    }
    return remember(launcher) {
        {
            val opts = ScanOptions()
                .setDesiredBarcodeFormats(ScanOptions.QR_CODE)
                .setBeepEnabled(false)
                
                
                .setOrientationLocked(true)
                .setCaptureActivity(PortraitCaptureActivity::class.java)
                .setPrompt("")
            launcher.launch(opts)
        }
    }
}
