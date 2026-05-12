package io.haoma.calculator.messenger.calls

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.media.AudioDeviceInfo
import android.media.AudioManager
import android.os.Build
import android.os.Handler
import android.os.Looper
import androidx.core.content.ContextCompat
import io.haoma.calculator.log.Logger
import java.util.concurrent.Executor
import io.haoma.calculator.messenger.CallEntry
import io.haoma.calculator.messenger.CallStatus
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.launchIn
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.onEach


class AudioRouter(
    private val app: Context,
    private val activeCallsSource: StateFlow<Map<String, CallEntry>>,
    private val bluetoothConnectGrantedSource: StateFlow<Boolean>,
) {
    private val scope = CoroutineScope(
        SupervisorJob() + Dispatchers.Main + Logger.coroutineExceptionHandler,
    )
    private val audio = app.getSystemService(Context.AUDIO_SERVICE) as AudioManager
    private val mainExecutor: Executor = Executor { r -> Handler(Looper.getMainLooper()).post(r) }

    
    private val commDeviceListener =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            AudioManager.OnCommunicationDeviceChangedListener { device ->
                val granted = bluetoothConnectGrantedSource.value
                val mapped = device?.let { d ->
                    val kind = kindFor(d.type) ?: return@let null
                    val label = when {
                        kind == AudioRoute.Kind.Bluetooth && !granted -> "Bluetooth device"
                        else -> defaultLabel(kind, d.productName?.toString().orEmpty())
                    }
                    AudioRoute(kind = kind, label = label, deviceId = d.id)
                }
                Logger.i("audio", "comm-device changed -> kind=${mapped?.kind} id=${mapped?.deviceId ?: -1}")
                _currentDevice.value = mapped
                
                
                if (_inCallActive.value) refresh()
            }
        } else null

    private val _inCallActive = MutableStateFlow(false)
    val inCallActive: StateFlow<Boolean> = _inCallActive.asStateFlow()

    private val _availableDevices = MutableStateFlow<List<AudioRoute>>(emptyList())
    val availableDevices: StateFlow<List<AudioRoute>> = _availableDevices.asStateFlow()

    private val _currentDevice = MutableStateFlow<AudioRoute?>(null)
    val currentDevice: StateFlow<AudioRoute?> = _currentDevice.asStateFlow()

    fun start() {
        activeCallsSource
            .map { calls -> calls.values.any { it.status == CallStatus.Accepted } }
            .distinctUntilChanged()
            .onEach { active ->
                if (active) onCallActive() else onCallInactive()
            }
            .launchIn(scope)
        
        
        bluetoothConnectGrantedSource
            .onEach { if (_inCallActive.value) refresh() }
            .launchIn(scope)
    }

    private fun onCallActive() {
        Logger.i("audio", "in-call active — switching MODE_IN_COMMUNICATION")
        _inCallActive.value = true
        audio.mode = AudioManager.MODE_IN_COMMUNICATION
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            commDeviceListener?.let {
                try {
                    audio.addOnCommunicationDeviceChangedListener(mainExecutor, it)
                } catch (t: Throwable) {
                    Logger.w("audio", "addOnCommunicationDeviceChangedListener: ${t.message}")
                }
            }
        }
        
        
        refresh()
    }

    private fun onCallInactive() {
        Logger.i("audio", "in-call inactive — restoring MODE_NORMAL")
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            commDeviceListener?.let {
                try {
                    audio.removeOnCommunicationDeviceChangedListener(it)
                } catch (t: Throwable) {
                    Logger.w("audio", "removeOnCommunicationDeviceChangedListener: ${t.message}")
                }
            }
            try {
                audio.clearCommunicationDevice()
            } catch (t: Throwable) {
                Logger.w("audio", "clearCommunicationDevice: ${t.message}")
            }
        } else {
            @Suppress("DEPRECATION")
            audio.isSpeakerphoneOn = false
        }
        audio.mode = AudioManager.MODE_NORMAL
        _inCallActive.value = false
        _availableDevices.value = emptyList()
        _currentDevice.value = null
    }

    
    fun refresh() {
        if (!_inCallActive.value) return
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            refreshApi31()
        } else {
            refreshLegacy()
        }
    }

    private fun refreshApi31() {
        val granted = bluetoothConnectGrantedSource.value
        val devices = try {
            audio.availableCommunicationDevices
        } catch (t: Throwable) {
            Logger.w("audio", "availableCommunicationDevices: ${t.message}")
            emptyList()
        }
        val mapped = devices.mapNotNull { device ->
            val kind = kindFor(device.type) ?: return@mapNotNull null
            
            
            val label = when {
                kind == AudioRoute.Kind.Bluetooth && !granted -> "Bluetooth device"
                else -> defaultLabel(kind, device.productName?.toString().orEmpty())
            }
            AudioRoute(kind = kind, label = label, deviceId = device.id)
        }
        _availableDevices.value = dedupe(mapped)
        val cur = audio.communicationDevice
        _currentDevice.value = _availableDevices.value.firstOrNull { it.deviceId == cur?.id }
    }

    private fun refreshLegacy() {
        
        
        @Suppress("DEPRECATION")
        val onSpeaker = audio.isSpeakerphoneOn
        val routes = listOf(
            AudioRoute(AudioRoute.Kind.Earpiece, "Phone", -1),
            AudioRoute(AudioRoute.Kind.Speaker, "Speaker", -1),
        )
        _availableDevices.value = routes
        _currentDevice.value = if (onSpeaker) routes[1] else routes[0]
    }

    
    fun routeTo(route: AudioRoute): Boolean {
        if (!_inCallActive.value) return false
        if (route.kind == AudioRoute.Kind.Bluetooth && !bluetoothConnectGrantedSource.value) {
            Logger.i("audio", "BT route requested without permission — caller prompts")
            return false
        }
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            routeApi31(route)
        } else {
            routeLegacy(route)
        }
    }

    private fun routeApi31(route: AudioRoute): Boolean {
        val devices = try {
            audio.availableCommunicationDevices
        } catch (t: Throwable) {
            Logger.w("audio", "routeTo enumerate: ${t.message}")
            return false
        }
        val target = devices.firstOrNull { it.id == route.deviceId }
            ?: devices.firstOrNull { kindFor(it.type) == route.kind }
        if (target == null) {
            Logger.w("audio", "routeTo no device for kind=${route.kind} id=${route.deviceId}")
            return false
        }
        val ok = try {
            audio.setCommunicationDevice(target)
        } catch (t: Throwable) {
            Logger.w("audio", "setCommunicationDevice: ${t.message}")
            false
        }
        Logger.i("audio", "routeTo kind=${route.kind} dev=${target.id} ok=$ok")
        if (ok) refresh()
        return ok
    }

    private fun routeLegacy(route: AudioRoute): Boolean {
        @Suppress("DEPRECATION")
        audio.isSpeakerphoneOn = route.kind == AudioRoute.Kind.Speaker
        Logger.i("audio", "routeTo legacy speakerphone=${route.kind == AudioRoute.Kind.Speaker}")
        refresh()
        return true
    }

    private fun kindFor(type: Int): AudioRoute.Kind? = when (type) {
        AudioDeviceInfo.TYPE_BUILTIN_EARPIECE -> AudioRoute.Kind.Earpiece
        AudioDeviceInfo.TYPE_BUILTIN_SPEAKER -> AudioRoute.Kind.Speaker
        AudioDeviceInfo.TYPE_WIRED_HEADSET,
        AudioDeviceInfo.TYPE_WIRED_HEADPHONES,
        AudioDeviceInfo.TYPE_USB_HEADSET -> AudioRoute.Kind.Wired
        AudioDeviceInfo.TYPE_BLUETOOTH_SCO,
        AudioDeviceInfo.TYPE_BLUETOOTH_A2DP -> AudioRoute.Kind.Bluetooth
        AudioDeviceInfo.TYPE_HEARING_AID,
        AudioDeviceInfo.TYPE_BLE_HEADSET -> AudioRoute.Kind.Bluetooth
        else -> null
    }

    private fun defaultLabel(kind: AudioRoute.Kind, name: String): String = when (kind) {
        AudioRoute.Kind.Earpiece -> "Phone"
        AudioRoute.Kind.Speaker -> "Speaker"
        AudioRoute.Kind.Wired -> if (name.isNotEmpty()) name else "Wired headset"
        AudioRoute.Kind.Bluetooth -> if (name.isNotEmpty()) name else "Bluetooth"
    }

    
    private fun dedupe(routes: List<AudioRoute>): List<AudioRoute> {
        val seen = HashSet<AudioRoute.Kind>()
        return routes.filter { seen.add(it.kind) || it.kind == AudioRoute.Kind.Bluetooth }
    }

    companion object {
        

        fun bluetoothConnectGranted(ctx: Context): Boolean {
            if (Build.VERSION.SDK_INT < Build.VERSION_CODES.S) return true
            return ContextCompat.checkSelfPermission(
                ctx,
                Manifest.permission.BLUETOOTH_CONNECT,
            ) == PackageManager.PERMISSION_GRANTED
        }
    }
}


data class AudioRoute(
    val kind: Kind,
    val label: String,
    val deviceId: Int,
) {
    enum class Kind { Earpiece, Speaker, Wired, Bluetooth }
}
