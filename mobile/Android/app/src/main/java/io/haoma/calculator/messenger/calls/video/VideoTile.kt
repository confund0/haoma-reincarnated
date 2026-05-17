package io.haoma.calculator.messenger.calls.video

import android.view.SurfaceHolder
import android.view.SurfaceView
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.calls.CallWindowTheme
import io.haoma.calculator.messenger.shortCallId
import kotlinx.coroutines.launch


@Composable
internal fun VideoTile(store: MessengerStore, callId: String, modifier: Modifier = Modifier) {
    val streamsByCall by store._videoStreams.collectAsState()
    val stream = streamsByCall[callId]?.get("vid")

    val peerMutedMap by store.peerVideoMutedCalls.collectAsState()
    val isPeerMuted = peerMutedMap[callId] == true

    var firstFramePainted by remember(callId) { mutableStateOf(false) }
    var syncing by remember(callId) { mutableStateOf(true) }

    val rendererRef = remember(callId) { mutableStateOf<VideoRenderer?>(null) }

    LaunchedEffect(rendererRef.value) {
        val r = rendererRef.value ?: return@LaunchedEffect
        launch { r.firstFrameAt.collect { firstFramePainted = (it != null) } }
        launch { r.syncing.collect { syncing = it } }
    }

    DisposableEffect(callId) {
        Logger.i(
            "call",
            "videotile mount call=${shortCallId(callId)} stream=${stream != null}",
        )
        onDispose {
            Logger.i("call", "videotile unmount call=${shortCallId(callId)}")
            rendererRef.value?.stop()
            rendererRef.value = null
        }
    }

    Box(modifier = modifier.fillMaxSize()) {
        AndroidView(
            modifier = Modifier.fillMaxSize(),
            factory = { ctx ->
                SurfaceView(ctx).apply {
                    holder.addCallback(object : SurfaceHolder.Callback {
                        override fun surfaceCreated(holder: SurfaceHolder) {
                            Logger.i(
                                "call",
                                "videotile surface_created call=${shortCallId(callId)}",
                            )
                            val w = width.coerceAtLeast(1)
                            val h = height.coerceAtLeast(1)
                            val r = VideoRenderer(
                                callId = callId,
                                streamProvider = {
                                    store._videoStreams.value[callId]?.get("vid")
                                },
                                clockSampleProvider = {
                                    store.callClockSamples.value[callId]
                                },
                            )
                            r.start(holder.surface, w, h)
                            rendererRef.value = r
                        }

                        override fun surfaceChanged(
                            holder: SurfaceHolder,
                            fmt: Int,
                            w: Int,
                            h: Int,
                        ) {
                            rendererRef.value?.resize(w, h)
                        }

                        override fun surfaceDestroyed(holder: SurfaceHolder) {
                            Logger.i(
                                "call",
                                "videotile surface_destroyed call=${shortCallId(callId)}",
                            )
                            rendererRef.value?.stop()
                            rendererRef.value = null
                        }
                    })
                }
            },
        )
        OverlayLayer(
            hasStream = stream != null,
            isPeerMuted = isPeerMuted,
            firstFramePainted = firstFramePainted,
            syncing = syncing,
        )
    }
}

@Composable
private fun OverlayLayer(
    hasStream: Boolean,
    isPeerMuted: Boolean,
    firstFramePainted: Boolean,
    syncing: Boolean,
) {
    val text: String? = when {
        isPeerMuted -> "Camera off"
        !hasStream -> "Connecting…"
        !firstFramePainted -> "No video"
        syncing -> "Syncing audio…"
        else -> null
    }
    if (text != null) {
        Box(
            modifier = Modifier.fillMaxSize(),
            contentAlignment = Alignment.Center,
        ) {
            Text(
                text = text,
                color = CallWindowTheme.Text,
                fontSize = 16.sp,
            )
        }
    }
}
