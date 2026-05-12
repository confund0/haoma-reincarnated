package io.haoma.calculator.messenger.calls

import android.Manifest
import android.os.Build
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.updateBluetoothConnectGranted


@Composable
fun CallAudioDialog(store: MessengerStore, onDismiss: () -> Unit) {
    val router = store.audioRouter ?: run {
        
        
        onDismiss()
        return
    }
    val devices by router.availableDevices.collectAsStateWithLifecycle()
    val current by router.currentDevice.collectAsStateWithLifecycle()
    val granted by store.bluetoothConnectGranted.collectAsStateWithLifecycle()

    
    var pendingBt by remember { mutableStateOf<AudioRoute?>(null) }
    val btLauncher = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.RequestPermission(),
    ) { ok ->
        Logger.i("audio", "dialog BT grant=$ok")
        store.updateBluetoothConnectGranted(ok)
        val target = pendingBt
        pendingBt = null
        if (ok && target != null) {
            router.refresh()
            
            
            router.routeTo(target)
            onDismiss()
        }
    }

    MaterialTheme(
        colorScheme = darkColorScheme(
            surface = DialogSurface,
            surfaceContainerHigh = DialogSurface,
            onSurface = DialogText,
            primary = DialogAccent,
        ),
    ) {
        AlertDialog(
            onDismissRequest = onDismiss,
            title = { Text(text = "Audio route", color = DialogText) },
            text = {
                if (devices.isEmpty()) {
                    Text(
                        text = "No audio devices listed by the system yet.",
                        color = DialogTextDim,
                    )
                } else {
                    Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
                        devices.forEach { route ->
                            DeviceRow(
                                route = route,
                                selected = route.deviceId == current?.deviceId &&
                                    route.kind == current?.kind,
                                onTap = {
                                    val needsBtPrompt =
                                        route.kind == AudioRoute.Kind.Bluetooth &&
                                            !granted &&
                                            Build.VERSION.SDK_INT >= Build.VERSION_CODES.S
                                    if (needsBtPrompt) {
                                        pendingBt = route
                                        btLauncher.launch(Manifest.permission.BLUETOOTH_CONNECT)
                                    } else {
                                        router.routeTo(route)
                                        onDismiss()
                                    }
                                },
                            )
                        }
                    }
                }
            },
            confirmButton = {
                TextButton(onClick = onDismiss) {
                    Text("Close", color = DialogAccent)
                }
            },
            containerColor = DialogSurface,
        )
    }
}

@Composable
private fun DeviceRow(route: AudioRoute, selected: Boolean, onTap: () -> Unit) {
    val glyph = when (route.kind) {
        AudioRoute.Kind.Earpiece -> CallIcons.Headset
        AudioRoute.Kind.Speaker -> CallIcons.VolumeUp
        AudioRoute.Kind.Wired -> CallIcons.Headphones
        AudioRoute.Kind.Bluetooth -> CallIcons.Bluetooth
    }
    val solid = fontAwesomeSolid()
    val brands = fontAwesomeBrands()
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(8.dp))
            .background(if (selected) DialogSelectedBg else Color.Transparent)
            .clickable(onClick = onTap)
            .padding(horizontal = 10.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(modifier = Modifier.size(24.dp), contentAlignment = Alignment.Center) {
            Text(
                text = glyph,
                color = DialogText,
                fontSize = 16.sp,
                fontFamily = if (isBrandsGlyph(glyph)) brands else solid,
            )
        }
        Spacer(modifier = Modifier.width(10.dp))
        Text(
            text = route.label,
            color = if (selected) DialogAccent else DialogText,
            fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Normal,
            fontSize = 14.sp,
        )
    }
}

private val DialogSurface = Color(0xFF32302F)
private val DialogText = Color(0xFFEBDBB2)
private val DialogTextDim = Color(0xFF7C6F64)
private val DialogAccent = Color(0xFFB8BB26)
private val DialogSelectedBg = Color(0xFF3C3836)
