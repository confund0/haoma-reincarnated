package io.haoma.calculator.messenger.invites

import android.graphics.Bitmap
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import com.google.zxing.BarcodeFormat
import com.journeyapps.barcodescanner.BarcodeEncoder


@Composable
fun QrOverlay(words: String, onDismiss: () -> Unit) {
    val qrBitmap = remember(words) { encodeQr(words) }

    Dialog(onDismissRequest = onDismiss) {
        Column(
            modifier = Modifier
                .clip(RoundedCornerShape(12.dp))
                .background(OVERLAY_BG)
                .padding(20.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(16.dp),
        ) {
            Text(
                text = "Scan to accept invite",
                color = OVERLAY_FG,
                fontSize = 14.sp,
                fontWeight = FontWeight.SemiBold,
            )
            Image(
                bitmap = qrBitmap.asImageBitmap(),
                contentDescription = "Invite QR code",
                modifier = Modifier
                    .fillMaxWidth()
                    .aspectRatio(1f)
                    .background(Color.White)
                    .padding(8.dp),
            )
            Text(
                text = words,
                color = OVERLAY_WORDS,
                fontFamily = FontFamily.Monospace,
                fontSize = 13.sp,
                fontWeight = FontWeight.SemiBold,
            )
            Spacer(modifier = Modifier.height(4.dp))
            Button(
                onClick = onDismiss,
                shape = RoundedCornerShape(8.dp),
                contentPadding = PaddingValues(horizontal = 24.dp, vertical = 8.dp),
                colors = ButtonDefaults.buttonColors(
                    containerColor = OVERLAY_BTN,
                    contentColor = OVERLAY_BG,
                ),
            ) {
                Text("Close", fontSize = 13.sp, fontWeight = FontWeight.SemiBold)
            }
        }
    }
}


private fun encodeQr(text: String): Bitmap {
    val encoder = BarcodeEncoder()
    return encoder.encodeBitmap(text, BarcodeFormat.QR_CODE, 512, 512)
}

private val OVERLAY_BG = Color(0xFF282828)
private val OVERLAY_FG = Color(0xFFEBDBB2)
private val OVERLAY_WORDS = Color(0xFFFABD2F)
private val OVERLAY_BTN = Color(0xFF83A598)
