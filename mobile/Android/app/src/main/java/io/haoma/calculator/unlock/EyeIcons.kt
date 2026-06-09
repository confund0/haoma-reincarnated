package io.haoma.calculator.unlock

import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.PathFillType
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.graphics.vector.path
import androidx.compose.ui.unit.dp


val EyeOpenVector: ImageVector by lazy {
    ImageVector.Builder(
        name = "EyeOpen",
        defaultWidth = 24.dp,
        defaultHeight = 24.dp,
        viewportWidth = 24f,
        viewportHeight = 24f,
    ).apply {
        
        
        path(
            fill = SolidColor(Color.White),
            pathFillType = PathFillType.EvenOdd,
        ) {
            
            moveTo(12f, 5f)
            curveTo(7f, 5f, 2.7f, 8.1f, 1f, 12f)
            curveTo(2.7f, 15.9f, 7f, 19f, 12f, 19f)
            curveTo(17f, 19f, 21.3f, 15.9f, 23f, 12f)
            curveTo(21.3f, 8.1f, 17f, 5f, 12f, 5f)
            close()
            
            moveTo(12f, 8f)
            curveTo(9.8f, 8f, 8f, 9.8f, 8f, 12f)
            curveTo(8f, 14.2f, 9.8f, 16f, 12f, 16f)
            curveTo(14.2f, 16f, 16f, 14.2f, 16f, 12f)
            curveTo(16f, 9.8f, 14.2f, 8f, 12f, 8f)
            close()
        }
        
        
        path(fill = SolidColor(Color.White)) {
            moveTo(12f, 10f)
            curveTo(10.9f, 10f, 10f, 10.9f, 10f, 12f)
            curveTo(10f, 13.1f, 10.9f, 14f, 12f, 14f)
            curveTo(13.1f, 14f, 14f, 13.1f, 14f, 12f)
            curveTo(14f, 10.9f, 13.1f, 10f, 12f, 10f)
            close()
        }
    }.build()
}

val EyeOffVector: ImageVector by lazy {
    ImageVector.Builder(
        name = "EyeOff",
        defaultWidth = 24.dp,
        defaultHeight = 24.dp,
        viewportWidth = 24f,
        viewportHeight = 24f,
    ).apply {
        
        path(
            fill = SolidColor(Color.White),
            pathFillType = PathFillType.EvenOdd,
        ) {
            moveTo(12f, 5f)
            curveTo(7f, 5f, 2.7f, 8.1f, 1f, 12f)
            curveTo(2.7f, 15.9f, 7f, 19f, 12f, 19f)
            curveTo(17f, 19f, 21.3f, 15.9f, 23f, 12f)
            curveTo(21.3f, 8.1f, 17f, 5f, 12f, 5f)
            close()
            moveTo(12f, 8f)
            curveTo(9.8f, 8f, 8f, 9.8f, 8f, 12f)
            curveTo(8f, 14.2f, 9.8f, 16f, 12f, 16f)
            curveTo(14.2f, 16f, 16f, 14.2f, 16f, 12f)
            curveTo(16f, 9.8f, 14.2f, 8f, 12f, 8f)
            close()
        }
        path(fill = SolidColor(Color.White)) {
            moveTo(12f, 10f)
            curveTo(10.9f, 10f, 10f, 10.9f, 10f, 12f)
            curveTo(10f, 13.1f, 10.9f, 14f, 12f, 14f)
            curveTo(13.1f, 14f, 14f, 13.1f, 14f, 12f)
            curveTo(14f, 10.9f, 13.1f, 10f, 12f, 10f)
            close()
        }
        
        
        path(
            stroke = SolidColor(Color.White),
            strokeLineWidth = 2.5f,
            strokeLineCap = StrokeCap.Round,
            strokeLineJoin = StrokeJoin.Round,
        ) {
            moveTo(4f, 4f)
            lineTo(20f, 20f)
        }
    }.build()
}
