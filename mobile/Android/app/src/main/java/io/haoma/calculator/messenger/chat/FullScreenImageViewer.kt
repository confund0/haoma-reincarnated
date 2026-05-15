package io.haoma.calculator.messenger.chat

import android.graphics.BitmapFactory
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.gestures.detectTransformGestures
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.compose.foundation.Image
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.ViewerTarget
import io.haoma.calculator.messenger.closeImageViewer
import io.haoma.calculator.messenger.openImageBytes
import kotlin.math.abs


@Composable
fun FullScreenImageViewer(store: MessengerStore, target: ViewerTarget) {
    val bytesMap by store.imageBytesByMsgId.collectAsStateWithLifecycle()
    val bytes = bytesMap[target.msgId]

    LaunchedEffect(target.msgId) {
        if (bytes == null) store.openImageBytes(target.chatId, target.msgId)
    }

    Dialog(
        onDismissRequest = { store.closeImageViewer() },
        properties = DialogProperties(
            usePlatformDefaultWidth = false,
            dismissOnBackPress = true,
            dismissOnClickOutside = false,
        ),
    ) {
        Box(
            modifier = Modifier
                .fillMaxSize()
                .background(Color.Black),
            contentAlignment = Alignment.Center,
        ) {
            if (bytes == null) {
                CircularProgressIndicator(color = Color.White)
                return@Box
            }
            val bitmap = remember(target.msgId, bytes) {
                runCatching { BitmapFactory.decodeByteArray(bytes, 0, bytes.size) }
                    .onFailure {
                        Logger.w(
                            "image-viewer",
                            "decode failed msg=${target.msgId} err=${it.message ?: "?"}",
                        )
                    }
                    .getOrNull()
            }
            if (bitmap == null) {
                Text(
                    text = "Could not display image.",
                    color = Color.White,
                    fontSize = 14.sp,
                    modifier = Modifier.padding(24.dp),
                )
                return@Box
            }

            var scale by remember { mutableStateOf(1f) }
            var offsetX by remember { mutableStateOf(0f) }
            var offsetY by remember { mutableStateOf(0f) }

            Image(
                bitmap = bitmap.asImageBitmap(),
                contentDescription = target.displayName,
                contentScale = ContentScale.Fit,
                modifier = Modifier
                    .fillMaxSize()
                    .graphicsLayer(
                        scaleX = scale,
                        scaleY = scale,
                        translationX = offsetX,
                        translationY = offsetY,
                        alpha = if (scale <= 1f && offsetY > 0f) {
                            (1f - (offsetY / DISMISS_THRESHOLD_PX).coerceIn(0f, 1f))
                        } else 1f,
                    )
                    .pointerInput(target.msgId) {
                        detectTapGestures(
                            onDoubleTap = {
                                if (scale > 1f) {
                                    scale = 1f; offsetX = 0f; offsetY = 0f
                                } else {
                                    scale = DOUBLE_TAP_SCALE
                                }
                            },
                        )
                    }
                    .pointerInput(target.msgId) {
                        detectTransformGestures { _, pan, zoom, _ ->
                            val nextScale = (scale * zoom).coerceIn(MIN_SCALE, MAX_SCALE)
                            
                            
                            if (nextScale <= 1f && abs(pan.x) < abs(pan.y)) {
                                scale = 1f
                                offsetX = 0f
                                offsetY = (offsetY + pan.y).coerceAtLeast(0f)
                                if (offsetY > DISMISS_THRESHOLD_PX) {
                                    store.closeImageViewer()
                                }
                            } else {
                                scale = nextScale
                                offsetX += pan.x
                                offsetY += pan.y
                            }
                        }
                    },
            )
        }
    }
}

private const val MIN_SCALE = 1f
private const val MAX_SCALE = 6f
private const val DOUBLE_TAP_SCALE = 2.5f
private const val DISMISS_THRESHOLD_PX = 250f
