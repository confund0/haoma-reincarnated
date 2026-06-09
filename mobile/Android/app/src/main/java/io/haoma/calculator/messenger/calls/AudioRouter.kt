package io.haoma.calculator.messenger.calls

import android.Manifest
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.media.AudioAttributes
import android.media.AudioDeviceCallback
import android.media.AudioDeviceInfo
import android.media.AudioFocusRequest
import android.media.AudioManager
import android.os.Build
import android.os.Handler
import android.os.Looper
import androidx.core.content.ContextCompat
import io.haoma.calculator.log.Logger
import java.util.concurrent.Executor
import io.haoma.calculator.messenger.CallEntry
import io.haoma.calculator.messenger.CallModality
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
                
                
                Logger.i(
                    "audio",
                    "comm-device changed -> kind=${mapped?.kind} id=${mapped?.deviceId ?: -1} type=${device?.type ?: -1}(${typeName(device?.type)})",
                )
                _currentDevice.value = mapped
                
                
                if (_inCallActive.value) {
                    dumpAllAudioDevices("comm-device-changed")
                    refresh()
                }
            }
        } else null

    private val _inCallActive = MutableStateFlow(false)
    val inCallActive: StateFlow<Boolean> = _inCallActive.asStateFlow()

    
    private var listenerRegistered = false

    
    private val scoReceiver = object : BroadcastReceiver() {
        override fun onReceive(ctx: Context?, intent: Intent?) {
            val state = intent?.getIntExtra(AudioManager.EXTRA_SCO_AUDIO_STATE, -2) ?: -2
            Logger.i("audio", "sco-state-changed state=$state(${scoStateName(state)})")
            if (_inCallActive.value) dumpAllAudioDevices("sco-state:$state")
            when (state) {
                AudioManager.SCO_AUDIO_STATE_CONNECTED -> {
                    scoRestartAttempts = 0
                    scheduleScoRefresh()
                }
                AudioManager.SCO_AUDIO_STATE_DISCONNECTED -> maybeRestartSco()
            }
        }
    }
    private var scoReceiverRegistered = false

    
    private val audioDeviceCallback = object : AudioDeviceCallback() {
        override fun onAudioDevicesAdded(added: Array<out AudioDeviceInfo>?) {
            val summary = added?.joinToString(prefix = "[", postfix = "]") {
                "id=${it.id} type=${it.type}(${typeName(it.type)})"
            } ?: "[]"
            Logger.i("audio", "device added $summary")
            if (_inCallActive.value) {
                dumpAllAudioDevices("device-added")
                refresh()
            }
        }
        override fun onAudioDevicesRemoved(removed: Array<out AudioDeviceInfo>?) {
            val summary = removed?.joinToString(prefix = "[", postfix = "]") {
                "id=${it.id} type=${it.type}(${typeName(it.type)})"
            } ?: "[]"
            Logger.i("audio", "device removed $summary")
            if (_inCallActive.value) {
                dumpAllAudioDevices("device-removed")
                refresh()
            }
        }
    }
    private var audioDeviceCallbackRegistered = false

    
    private var btRouteDesired = false
    private var scoRestartAttempts = 0
    private val scoRestartHandler = Handler(Looper.getMainLooper())

    
    private val audioFocusListener = AudioManager.OnAudioFocusChangeListener { change ->
        val name = when (change) {
            AudioManager.AUDIOFOCUS_GAIN -> "GAIN"
            AudioManager.AUDIOFOCUS_GAIN_TRANSIENT -> "GAIN_TRANSIENT"
            AudioManager.AUDIOFOCUS_GAIN_TRANSIENT_EXCLUSIVE -> "GAIN_TRANSIENT_EXCLUSIVE"
            AudioManager.AUDIOFOCUS_GAIN_TRANSIENT_MAY_DUCK -> "GAIN_TRANSIENT_MAY_DUCK"
            AudioManager.AUDIOFOCUS_LOSS -> "LOSS"
            AudioManager.AUDIOFOCUS_LOSS_TRANSIENT -> "LOSS_TRANSIENT"
            AudioManager.AUDIOFOCUS_LOSS_TRANSIENT_CAN_DUCK -> "LOSS_TRANSIENT_CAN_DUCK"
            else -> "OTHER($change)"
        }
        Logger.i("audio", "audio-focus change: $name")
    }
    private var audioFocusRequest: AudioFocusRequest? = null

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
        
        
        requestAudioFocus()
        audio.mode = AudioManager.MODE_IN_COMMUNICATION
        
        
        dumpAllAudioDevices("call-start")
        registerScoReceiver()
        registerAudioDeviceCallback()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            commDeviceListener?.let {
                try {
                    audio.addOnCommunicationDeviceChangedListener(mainExecutor, it)
                    listenerRegistered = true
                } catch (t: Throwable) {
                    Logger.w("audio", "addOnCommunicationDeviceChangedListener: ${t.message}")
                }
            }
            
            
            try {
                val all = audio.availableCommunicationDevices
                val current = audio.communicationDevice
                Logger.i(
                    "audio",
                    "available comm devices: " +
                        all.joinToString(prefix = "[", postfix = "]") {
                            "id=${it.id} type=${it.type}(${typeName(it.type)})"
                        } +
                        " current=id=${current?.id ?: -1} type=${current?.type ?: -1}(${typeName(current?.type)})",
                )
            } catch (t: Throwable) {
                Logger.w("audio", "enumerate comm devices: ${t.message}")
            }
        }
        
        
        refresh()
        if (activeCallsSource.value.values.any {
                it.status == CallStatus.Accepted && CallModality.Video in it.modalities
            }) {
            pickVideoDefaultRoute()
        }
    }

    
    private fun pickVideoDefaultRoute() {
        val available = _availableDevices.value
        val btGranted = bluetoothConnectGrantedSource.value
        val pick = available.firstOrNull {
            it.kind == AudioRoute.Kind.Bluetooth && btGranted
        }
            ?: available.firstOrNull { it.kind == AudioRoute.Kind.Wired }
            ?: available.firstOrNull { it.kind == AudioRoute.Kind.Speaker }
        if (pick == null) {
            Logger.w("audio", "video-call default route: no eligible device in $available")
            return
        }
        Logger.i("audio", "video-call default route → kind=${pick.kind} id=${pick.deviceId}")
        routeTo(pick)
    }

    private fun onCallInactive() {
        Logger.i("audio", "in-call inactive — restoring MODE_NORMAL")
        unregisterAudioDeviceCallback()
        unregisterScoReceiver()
        scoRestartHandler.removeCallbacksAndMessages(null)
        btRouteDesired = false
        
        
        stopScoIfOn("call-inactive")
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            if (listenerRegistered) {
                commDeviceListener?.let {
                    try {
                        audio.removeOnCommunicationDeviceChangedListener(it)
                    } catch (t: Throwable) {
                        Logger.w("audio", "removeOnCommunicationDeviceChangedListener: ${t.message}")
                    }
                }
                listenerRegistered = false
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
        
        
        abandonAudioFocus()
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
        
        
        if (route.kind == AudioRoute.Kind.Bluetooth) {
            btRouteDesired = true
            scoRestartAttempts = 0
            startScoIfNeeded("route-pick-bt")
        } else {
            btRouteDesired = false
            scoRestartHandler.removeCallbacks(scoRefreshRunnable)
            stopScoIfOn("route-pick-${route.kind}")
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

    
    private fun typeName(type: Int?): String = when (type) {
        null -> "null"
        AudioDeviceInfo.TYPE_BUILTIN_EARPIECE -> "BUILTIN_EARPIECE"
        AudioDeviceInfo.TYPE_BUILTIN_SPEAKER -> "BUILTIN_SPEAKER"
        AudioDeviceInfo.TYPE_BUILTIN_MIC -> "BUILTIN_MIC"
        AudioDeviceInfo.TYPE_WIRED_HEADSET -> "WIRED_HEADSET"
        AudioDeviceInfo.TYPE_WIRED_HEADPHONES -> "WIRED_HEADPHONES"
        AudioDeviceInfo.TYPE_USB_HEADSET -> "USB_HEADSET"
        AudioDeviceInfo.TYPE_USB_DEVICE -> "USB_DEVICE"
        AudioDeviceInfo.TYPE_BLUETOOTH_SCO -> "BLUETOOTH_SCO"
        AudioDeviceInfo.TYPE_BLUETOOTH_A2DP -> "BLUETOOTH_A2DP"
        AudioDeviceInfo.TYPE_HEARING_AID -> "HEARING_AID"
        AudioDeviceInfo.TYPE_BLE_HEADSET -> "BLE_HEADSET"
        else -> "other"
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

    
    private fun dumpAllAudioDevices(label: String) {
        try {
            val ins = audio.getDevices(AudioManager.GET_DEVICES_INPUTS)
            val outs = audio.getDevices(AudioManager.GET_DEVICES_OUTPUTS)
            @Suppress("DEPRECATION")
            val scoOn = audio.isBluetoothScoOn
            @Suppress("DEPRECATION")
            val scoAvail = audio.isBluetoothScoAvailableOffCall
            Logger.i(
                "audio",
                "devices[$label] mode=${audio.mode} sco_on=$scoOn sco_avail=$scoAvail " +
                    "inputs=" + ins.joinToString(prefix = "[", postfix = "]", separator = ",") {
                        "id=${it.id} type=${it.type}(${typeName(it.type)})"
                    } +
                    " outputs=" + outs.joinToString(prefix = "[", postfix = "]", separator = ",") {
                        "id=${it.id} type=${it.type}(${typeName(it.type)})"
                    },
            )
        } catch (t: Throwable) {
            Logger.w("audio", "dumpAllAudioDevices($label): ${t.message}")
        }
    }

    private fun registerScoReceiver() {
        if (scoReceiverRegistered) return
        try {
            val filter = IntentFilter(AudioManager.ACTION_SCO_AUDIO_STATE_UPDATED)
            
            
            ContextCompat.registerReceiver(
                app,
                scoReceiver,
                filter,
                ContextCompat.RECEIVER_EXPORTED,
            )
            scoReceiverRegistered = true
        } catch (t: Throwable) {
            Logger.w("audio", "registerScoReceiver: ${t.message}")
        }
    }

    private fun unregisterScoReceiver() {
        if (!scoReceiverRegistered) return
        try {
            app.unregisterReceiver(scoReceiver)
        } catch (t: Throwable) {
            Logger.w("audio", "unregisterScoReceiver: ${t.message}")
        }
        scoReceiverRegistered = false
    }

    private fun registerAudioDeviceCallback() {
        if (audioDeviceCallbackRegistered) return
        try {
            audio.registerAudioDeviceCallback(
                audioDeviceCallback,
                Handler(Looper.getMainLooper()),
            )
            audioDeviceCallbackRegistered = true
        } catch (t: Throwable) {
            Logger.w("audio", "registerAudioDeviceCallback: ${t.message}")
        }
    }

    private fun unregisterAudioDeviceCallback() {
        if (!audioDeviceCallbackRegistered) return
        try {
            audio.unregisterAudioDeviceCallback(audioDeviceCallback)
        } catch (t: Throwable) {
            Logger.w("audio", "unregisterAudioDeviceCallback: ${t.message}")
        }
        audioDeviceCallbackRegistered = false
    }

    private fun requestAudioFocus() {
        if (audioFocusRequest != null) return
        try {
            val attrs = AudioAttributes.Builder()
                .setUsage(AudioAttributes.USAGE_VOICE_COMMUNICATION)
                .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH)
                .build()
            val req = AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN_TRANSIENT)
                .setAudioAttributes(attrs)
                .setOnAudioFocusChangeListener(audioFocusListener, Handler(Looper.getMainLooper()))
                .build()
            val res = audio.requestAudioFocus(req)
            audioFocusRequest = req
            val name = when (res) {
                AudioManager.AUDIOFOCUS_REQUEST_GRANTED -> "GRANTED"
                AudioManager.AUDIOFOCUS_REQUEST_FAILED -> "FAILED"
                AudioManager.AUDIOFOCUS_REQUEST_DELAYED -> "DELAYED"
                else -> "OTHER($res)"
            }
            Logger.i("audio", "audio-focus request: $name")
        } catch (t: Throwable) {
            Logger.w("audio", "requestAudioFocus: ${t.message}")
        }
    }

    private fun abandonAudioFocus() {
        val req = audioFocusRequest ?: return
        try {
            val res = audio.abandonAudioFocusRequest(req)
            Logger.i("audio", "audio-focus abandon: $res")
        } catch (t: Throwable) {
            Logger.w("audio", "abandonAudioFocus: ${t.message}")
        }
        audioFocusRequest = null
    }

    
    @Suppress("DEPRECATION")
    private fun startScoIfNeeded(reason: String) {
        if (audio.isBluetoothScoOn) {
            Logger.i("audio", "BT SCO already on ($reason)")
            return
        }
        try {
            audio.startBluetoothSco()
            audio.isBluetoothScoOn = true
            Logger.i("audio", "BT SCO start ($reason)")
        } catch (t: Throwable) {
            Logger.w("audio", "startBluetoothSco ($reason): ${t.message}")
        }
    }

    
    @Suppress("DEPRECATION")
    private val scoRefreshRunnable = Runnable {
        if (!_inCallActive.value || !btRouteDesired) return@Runnable
        Logger.i("audio", "BT SCO: pre-emptive refresh")
        try {
            audio.startBluetoothSco()
        } catch (t: Throwable) {
            Logger.w("audio", "BT SCO refresh: ${t.message}")
        }
        scheduleScoRefresh()
    }

    private fun scheduleScoRefresh() {
        scoRestartHandler.removeCallbacks(scoRefreshRunnable)
        scoRestartHandler.postDelayed(scoRefreshRunnable, SCO_REFRESH_INTERVAL_MS)
    }

    private fun maybeRestartSco() {
        if (!_inCallActive.value || !btRouteDesired) return
        if (scoRestartAttempts >= MAX_SCO_RESTART_ATTEMPTS) {
            Logger.w("audio", "BT SCO: gave up auto-restart after $scoRestartAttempts attempts")
            return
        }
        scoRestartAttempts++
        Logger.i("audio", "BT SCO: drop while desired; auto-restart attempt $scoRestartAttempts")
        scoRestartHandler.postDelayed({
            if (_inCallActive.value && btRouteDesired) {
                startScoIfNeeded("sco-auto-restart-$scoRestartAttempts")
            }
        }, 250L)
    }

    @Suppress("DEPRECATION")
    private fun stopScoIfOn(reason: String) {
        if (!audio.isBluetoothScoOn) return
        try {
            audio.stopBluetoothSco()
            audio.isBluetoothScoOn = false
            Logger.i("audio", "BT SCO stop ($reason)")
        } catch (t: Throwable) {
            Logger.w("audio", "stopBluetoothSco ($reason): ${t.message}")
        }
    }

    private fun scoStateName(state: Int): String = when (state) {
        AudioManager.SCO_AUDIO_STATE_DISCONNECTED -> "DISCONNECTED"
        AudioManager.SCO_AUDIO_STATE_CONNECTED -> "CONNECTED"
        AudioManager.SCO_AUDIO_STATE_CONNECTING -> "CONNECTING"
        AudioManager.SCO_AUDIO_STATE_ERROR -> "ERROR"
        else -> "other"
    }

    companion object {
        
        
        private const val MAX_SCO_RESTART_ATTEMPTS = 2

        
        private const val SCO_REFRESH_INTERVAL_MS = 50_000L

        
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
