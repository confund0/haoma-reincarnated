package io.haoma.calculator.messenger.chat

import android.widget.VideoView
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectVerticalDragGestures
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.Slider
import androidx.compose.material3.SliderDefaults
import androidx.compose.material3.Text
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.VideoViewerTarget
import io.haoma.calculator.messenger.closeVideoViewer
import kotlinx.coroutines.delay
import kotlin.math.abs


@Composable
fun FullScreenVideoViewer(store: MessengerStore, target: VideoViewerTarget) {
    FullScreenOverlay(
        onDismiss = { store.closeVideoViewer() },
        background = Color.Black,
        contentAlignment = Alignment.Center,
    ) {
        VideoViewerContent(store = store, target = target)
    }
}

@Composable
private fun VideoViewerContent(store: MessengerStore, target: VideoViewerTarget) {
    val context = LocalContext.current
    var videoView by remember(target.path) { mutableStateOf<VideoView?>(null) }
    var playing by remember(target.path) { mutableStateOf(true) }
    var positionMs by remember(target.path) { mutableStateOf(0) }
    var durationMs by remember(target.path) { mutableStateOf(0) }
    var offsetY by remember(target.path) { mutableStateOf(0f) }

    
    LaunchedEffect(videoView) {
        val vv = videoView ?: return@LaunchedEffect
        while (true) {
            if (durationMs == 0 && vv.duration > 0) durationMs = vv.duration
            if (vv.isPlaying) positionMs = vv.currentPosition
            delay(200L)
        }
    }

    DisposableEffect(target.path) {
        onDispose { videoView?.stopPlayback() }
    }

    val interactionSource = remember { MutableInteractionSource() }
    Box(
        modifier = Modifier
            .fillMaxSize()
            .background(Color.Black)
            .graphicsLayer(
                translationY = offsetY,
                alpha = (1f - (offsetY / DISMISS_THRESHOLD_PX).coerceIn(0f, 1f)),
            )
            .pointerInput(target.path) {
                detectVerticalDragGestures(
                    onDragEnd = {
                        if (offsetY > DISMISS_THRESHOLD_PX) {
                            store.closeVideoViewer()
                        } else {
                            offsetY = 0f
                        }
                    },
                    onDragCancel = { offsetY = 0f },
                    onVerticalDrag = { _, dragAmount ->
                        offsetY = (offsetY + dragAmount).coerceAtLeast(0f)
                    },
                )
            },
        contentAlignment = Alignment.Center,
    ) {
        AndroidView(
            factory = { ctx ->
                VideoView(ctx).apply {
                    setOnPreparedListener { mp ->
                        mp.isLooping = false
                        durationMs = duration
                        start()
                    }
                    setOnCompletionListener {
                        playing = false
                    }
                    setOnErrorListener { _, what, extra ->
                        Logger.w(
                            "video-viewer",
                            "play error msg=${target.msgId} what=$what extra=$extra",
                        )
                        
                        
                        launchView(context, target.path, target.mime.ifEmpty { "video/*" })
                        store.closeVideoViewer()
                        true
                    }
                    setVideoPath(target.path)
                }.also { videoView = it }
            },
            modifier = Modifier
                .fillMaxSize()
                .clickable(
                    interactionSource = interactionSource,
                    indication = null,
                    onClick = {
                        val vv = videoView ?: return@clickable
                        if (vv.isPlaying) {
                            vv.pause(); playing = false
                        } else {
                            vv.start(); playing = true
                        }
                    },
                ),
        )

        
        Column(
            modifier = Modifier
                .align(Alignment.TopStart)
                .fillMaxWidth()
                .background(Color.Black.copy(alpha = 0.45f))
                .padding(horizontal = 8.dp, vertical = 8.dp),
        ) {
            Box(modifier = Modifier.fillMaxWidth()) {
                IconButton(
                    onClick = { store.closeVideoViewer() },
                    modifier = Modifier.align(Alignment.CenterStart),
                ) {
                    Icon(
                        Icons.Filled.Close,
                        contentDescription = "Close",
                        tint = Color.White,
                    )
                }
                Text(
                    text = target.displayName.ifEmpty { "(video)" },
                    color = Color.White,
                    fontSize = 14.sp,
                    modifier = Modifier
                        .align(Alignment.Center)
                        .padding(horizontal = 56.dp),
                )
            }
        }

        
        Column(
            modifier = Modifier
                .align(Alignment.BottomCenter)
                .fillMaxWidth()
                .background(Color.Black.copy(alpha = 0.55f))
                .padding(horizontal = 8.dp, vertical = 6.dp),
        ) {
            Box(modifier = Modifier.fillMaxWidth()) {
                IconButton(
                    onClick = {
                        val vv = videoView ?: return@IconButton
                        if (vv.isPlaying) {
                            vv.pause(); playing = false
                        } else {
                            vv.start(); playing = true
                        }
                    },
                    modifier = Modifier.align(Alignment.CenterStart),
                ) {
                    if (playing) {
                        PauseGlyph()
                    } else {
                        Icon(
                            Icons.Filled.PlayArrow,
                            contentDescription = "Play",
                            tint = Color.White,
                            modifier = Modifier.size(28.dp),
                        )
                    }
                }
                Text(
                    text = formatTime(positionMs) + " / " + formatTime(durationMs),
                    color = Color.White,
                    fontSize = 12.sp,
                    modifier = Modifier
                        .align(Alignment.CenterEnd)
                        .padding(end = 8.dp),
                )
            }
            Slider(
                value = if (durationMs > 0) positionMs.toFloat() / durationMs.toFloat() else 0f,
                onValueChange = { frac ->
                    val vv = videoView ?: return@Slider
                    if (durationMs > 0) {
                        val target = (frac * durationMs).toInt().coerceIn(0, durationMs)
                        vv.seekTo(target)
                        positionMs = target
                    }
                },
                colors = SliderDefaults.colors(
                    thumbColor = Color.White,
                    activeTrackColor = Color.White,
                    inactiveTrackColor = Color.White.copy(alpha = 0.35f),
                ),
            )
        }
    }

    
    DisposableEffect(target.path) {
        onDispose {
            if (abs(offsetY) > 0f) offsetY = 0f
        }
    }
}


@Composable
private fun PauseGlyph() {
    Row(horizontalArrangement = Arrangement.spacedBy(5.dp)) {
        Box(
            modifier = Modifier
                .width(5.dp)
                .height(22.dp)
                .background(Color.White),
        )
        Box(
            modifier = Modifier
                .width(5.dp)
                .height(22.dp)
                .background(Color.White),
        )
    }
}

private fun formatTime(ms: Int): String {
    if (ms <= 0) return "0:00"
    val s = ms / 1000
    val m = s / 60
    val r = s % 60
    return "%d:%02d".format(m, r)
}

private const val DISMISS_THRESHOLD_PX = 250f
