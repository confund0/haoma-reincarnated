package io.haoma.calculator.messenger.invites

import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.graphics.vector.path
import androidx.compose.ui.unit.dp


val QrScannerVector: ImageVector by lazy {
    ImageVector.Builder(
        name = "QrScanner",
        defaultWidth = 24.dp,
        defaultHeight = 24.dp,
        viewportWidth = 24f,
        viewportHeight = 24f,
    ).apply {
        path(fill = SolidColor(Color.White)) {
            
            moveTo(3f, 3f); lineTo(10f, 3f); lineTo(10f, 5f); lineTo(5f, 5f)
            lineTo(5f, 10f); lineTo(3f, 10f); close()
            
            moveTo(14f, 3f); lineTo(21f, 3f); lineTo(21f, 10f); lineTo(19f, 10f)
            lineTo(19f, 5f); lineTo(14f, 5f); close()
            
            moveTo(3f, 14f); lineTo(5f, 14f); lineTo(5f, 19f); lineTo(10f, 19f)
            lineTo(10f, 21f); lineTo(3f, 21f); close()
            
            moveTo(19f, 14f); lineTo(21f, 14f); lineTo(21f, 21f); lineTo(14f, 21f)
            lineTo(14f, 19f); lineTo(19f, 19f); close()
            
            moveTo(7f, 11.25f); lineTo(17f, 11.25f); lineTo(17f, 12.75f); lineTo(7f, 12.75f); close()
        }
    }.build()
}
