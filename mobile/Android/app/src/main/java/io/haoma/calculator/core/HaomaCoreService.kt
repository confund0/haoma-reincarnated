package io.haoma.calculator.core

import android.Manifest
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import android.os.PowerManager
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import io.haoma.calculator.HaomaApp
import io.haoma.calculator.R
import io.haoma.calculator.log.Logger
import java.io.File
import java.util.concurrent.atomic.AtomicBoolean
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch


class HaomaCoreService : Service() {
    private val scope = CoroutineScope(
        SupervisorJob() + Dispatchers.IO + Logger.coroutineExceptionHandler,
    )

    @Volatile private var haomad: Daemon? = null
    @Volatile private var haoma: Daemon? = null

    
    @Volatile private var haomadAddr: String? = null
    @Volatile private var haomaAddr: String? = null

    
    private val daemonOpInFlight = AtomicBoolean(false)

    
    private var screenOffReceiver: BroadcastReceiver? = null

    
    private var wakeLock: PowerManager.WakeLock? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        Logger.i("fgs", "HaomaCoreService.onCreate")
        ensureChannel()
        registerScreenOffReceiver()
        acquireWakeLock()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val action = intent?.action
        Logger.i("fgs", "HaomaCoreService.onStartCommand startId=$startId flags=$flags action=$action")
        startForegroundCompat()
        when (action) {
            ACTION_REFRESH_TYPE -> {
                
                
            }
            ACTION_RESTART_DAEMONS -> {
                
                
                if (acquireDaemonOp("RESTART_DAEMONS")) {
                    scope.launch {
                        try { restartBothDaemons() } finally { daemonOpInFlight.set(false) }
                    }
                }
            }
            else -> {
                
                
                if (haomad == null || haoma == null) {
                    if (acquireDaemonOp("default")) {
                        scope.launch {
                            try { ensureDaemonsUp() } finally { daemonOpInFlight.set(false) }
                        }
                    }
                }
            }
        }
        return START_NOT_STICKY
    }

    
    override fun onTaskRemoved(rootIntent: Intent?) {
        Logger.i("fgs", "HaomaCoreService.onTaskRemoved")
        (application as? HaomaApp)?.idleLockDispatcher?.fire("swipe-up-kill")
        super.onTaskRemoved(rootIntent)
    }

    override fun onDestroy() {
        Logger.i("fgs", "HaomaCoreService.onDestroy")
        unregisterScreenOffReceiver()
        releaseWakeLock()
        
        
        try {
            (application as? HaomaApp)?.proximityController?.releaseLockIfHeld()
        } catch (t: Throwable) {
            Logger.e("fgs", "proximity release", t)
        }
        
        
        try {
            (application as? HaomaApp)?.messengerStore?.onDaemonsStopped()
        } catch (t: Throwable) {
            Logger.e("fgs", "messenger store teardown", t)
        }
        
        
        haoma?.let { d ->
            try {
                d.stop(STOP_GRACE_MS)
            } catch (t: Throwable) {
                Logger.e("fgs", "haoma stop", t)
            }
        }
        haoma = null
        haomaAddr = null
        haomad?.let { d ->
            try {
                d.stop(STOP_GRACE_MS)
            } catch (t: Throwable) {
                Logger.e("fgs", "haomad stop", t)
            }
        }
        haomad = null
        haomadAddr = null
        scope.cancel()
        super.onDestroy()
    }

    
    private fun ensureDaemonsUp() {
        
        
        reapDaemonOrphans(VaultHelper.cfgDir(applicationContext))
        reapStaleHandles()
        val claimed = BootstrapPayload.take()
        if (claimed == null) {
            Logger.w("fgs", "ensureDaemonsUp without a deposited payload; stopping")
            stopSelf()
            return
        }
        val (secrets, ack) = claimed
        var spawnedHaomad = false
        var spawnedHaoma = false
        try {
            if (haomad == null) {
                val d = spawnHaomad(secrets)
                haomad = d
                spawnedHaomad = true
                val addr = d.waitReady(READY_TIMEOUT_MS)
                haomadAddr = addr
                Logger.i("fgs", "haomad spawned api_addr=$addr")
            } else {
                Logger.i("fgs", "haomad already alive @$haomadAddr; reusing")
            }
            val haomadAddrLocal = haomadAddr
                ?: error("haomad addr null after liveness check")

            if (haoma == null) {
                val d = spawnHaoma(secrets, haomadAddrLocal)
                haoma = d
                spawnedHaoma = true
                val addr = d.waitReady(READY_TIMEOUT_MS)
                haomaAddr = addr
                Logger.i("fgs", "haoma spawned api_addr=$addr")
                attachMessengerStore(addr)
            } else {
                Logger.i("fgs", "haoma already alive @$haomaAddr; reusing")
            }

            ack.complete(BootstrapPayload.Result.Ok(haomadAddrLocal, haomaAddr ?: ""))
        } catch (t: Throwable) {
            Logger.e("fgs", "ensureDaemonsUp failed (spawnedHaomad=$spawnedHaomad spawnedHaoma=$spawnedHaoma)", t)
            ack.complete(BootstrapPayload.Result.Fail(t.message ?: t.javaClass.simpleName))
            
            if (spawnedHaoma) {
                runCatching { (application as? HaomaApp)?.messengerStore?.onDaemonsStopped() }
                haoma?.let { runCatching { it.stop(STOP_GRACE_MS) } }
                haoma = null
                haomaAddr = null
            }
            if (spawnedHaomad) {
                haomad?.let { runCatching { it.stop(STOP_GRACE_MS) } }
                haomad = null
                haomadAddr = null
            }
            
            
            if (haomad == null) {
                stopSelf()
            }
        } finally {
            
            
            secrets.fill(0)
        }
    }

    
    private fun acquireDaemonOp(label: String): Boolean {
        if (daemonOpInFlight.compareAndSet(false, true)) return true
        Logger.w("fgs", "$label dropped: another daemon op in flight")
        return false
    }

    
    private fun reapStaleHandles() {
        haoma?.let { d ->
            if (!d.isAlive) {
                Logger.w("fgs", "stale haoma handle (process exited); reaping")
                runCatching { d.stop(STOP_GRACE_MS) }
                haoma = null
                haomaAddr = null
                runCatching { (application as? HaomaApp)?.messengerStore?.onDaemonsStopped() }
            }
        }
        haomad?.let { d ->
            if (!d.isAlive) {
                Logger.w("fgs", "stale haomad handle (process exited); reaping")
                runCatching { d.stop(STOP_GRACE_MS) }
                haomad = null
                haomadAddr = null
                
                
                haoma?.let { runCatching { it.stop(STOP_GRACE_MS) } }
                haoma = null
                haomaAddr = null
                runCatching { (application as? HaomaApp)?.messengerStore?.onDaemonsStopped() }
            }
        }
    }

    
    private fun restartBothDaemons() {
        Logger.i("fgs", "ACTION_RESTART_DAEMONS tearing down haoma + haomad")
        runCatching { (application as? HaomaApp)?.messengerStore?.onDaemonsStopped() }
        haoma?.let { d ->
            try {
                d.stop(STOP_GRACE_MS)
            } catch (t: Throwable) {
                Logger.e("fgs", "haoma stop (restart)", t)
            }
        }
        haoma = null
        haomaAddr = null
        haomad?.let { d ->
            try {
                d.stop(STOP_GRACE_MS)
            } catch (t: Throwable) {
                Logger.e("fgs", "haomad stop (restart)", t)
            }
        }
        haomad = null
        haomadAddr = null
        Logger.i("fgs", "ACTION_RESTART_DAEMONS respawning via ensureDaemonsUp")
        ensureDaemonsUp()
    }

    private fun spawnHaomad(secretsBlob: ByteArray): Daemon {
        val nativeDir = applicationInfo.nativeLibraryDir
        val cfg = VaultHelper.cfgDir(applicationContext)
        val haomadLog = File(Logger.fileFor("haomad"))
        
        
        val torBin = File(nativeDir, "libtor.so")
        val torDataDir = File(cfg, "tor")
        val args = listOf(
            "--cfg-dir", cfg.absolutePath,
            "--secrets-stdin",
            "--api-addr", "127.0.0.1:0",
            "--runtime-file", File(cfg, "haomad.runtime.json").absolutePath,
            "--manage-tor", torBin.absolutePath,
            "--tor-data-dir", torDataDir.absolutePath,
            "--log-level", Logger.suiteLogLevel,
            "--log-file", haomadLog.absolutePath,
        )
        return Daemon.spawn(
            name = "haomad",
            bin = File(nativeDir, "libhaomad.so"),
            args = args,
            secretsBlob = secretsBlob,
            errLog = haomadLog,
        )
    }

    
    private fun spawnHaoma(secretsBlob: ByteArray, haomadAddr: String): Daemon {
        val nativeDir = applicationInfo.nativeLibraryDir
        val cfg = VaultHelper.cfgDir(applicationContext)
        val haomaLog = File(Logger.fileFor("haoma"))
        val args = listOf(
            "--cfg-dir", cfg.absolutePath,
            "--secrets-stdin",
            "--addr", "127.0.0.1:0",
            "--backend-addr", "https://$haomadAddr",
            "--log-level", Logger.suiteLogLevel,
            "--log-file", haomaLog.absolutePath,
        )
        return Daemon.spawn(
            name = "haoma",
            bin = File(nativeDir, "libhaoma.so"),
            args = args,
            secretsBlob = secretsBlob,
            errLog = haomaLog,
            
            
            pidFile = File(cfg, "haoma.pid"),
        )
    }

    
    private fun attachMessengerStore(haomaAddr: String) {
        val app = application as? HaomaApp ?: run {
            Logger.w("fgs", "attachMessengerStore: application is not HaomaApp")
            return
        }
        val frontendDir = File(VaultHelper.cfgDir(applicationContext), "frontend")
        app.messengerStore.onDaemonsReady(haomaAddr = haomaAddr, frontendDir = frontendDir)
    }

    private fun startForegroundCompat() {
        val notification = NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle(getString(R.string.app_name))
            .setContentText(getString(R.string.fgs_core_notification_text))
            .setSmallIcon(android.R.drawable.ic_menu_info_details)
            .setOngoing(true)
            .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .build()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            val type = pickForegroundServiceType()
            Logger.i("fgs", "startForeground type=${typeName(type)}")
            startForeground(NOTIF_ID, notification, type)
        } else {
            startForeground(NOTIF_ID, notification)
        }
    }

    
    private fun pickForegroundServiceType(): Int {
        val micGranted = ContextCompat.checkSelfPermission(
            this,
            Manifest.permission.RECORD_AUDIO,
        ) == PackageManager.PERMISSION_GRANTED
        val cameraGranted = ContextCompat.checkSelfPermission(
            this,
            Manifest.permission.CAMERA,
        ) == PackageManager.PERMISSION_GRANTED
        val videoCallActive =
            (application as? HaomaApp)?.messengerStore?.videoCallActive?.value == true
        if (micGranted) {
            var bitmap = ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE
            if (cameraGranted && videoCallActive) {
                bitmap = bitmap or ServiceInfo.FOREGROUND_SERVICE_TYPE_CAMERA
            }
            return bitmap
        }
        val dozeExempt = (getSystemService(POWER_SERVICE) as? PowerManager)
            ?.isIgnoringBatteryOptimizations(packageName) == true
        if (dozeExempt) {
            return ServiceInfo.FOREGROUND_SERVICE_TYPE_SYSTEM_EXEMPTED
        }
        return ServiceInfo.FOREGROUND_SERVICE_TYPE_REMOTE_MESSAGING
    }

    private fun typeName(type: Int): String {
        if (type == 0) return "none"
        val parts = mutableListOf<String>()
        if (type and ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE != 0) parts += "microphone"
        if (type and ServiceInfo.FOREGROUND_SERVICE_TYPE_CAMERA != 0) parts += "camera"
        if (type and ServiceInfo.FOREGROUND_SERVICE_TYPE_SYSTEM_EXEMPTED != 0) parts += "systemExempted"
        if (type and ServiceInfo.FOREGROUND_SERVICE_TYPE_REMOTE_MESSAGING != 0) parts += "remoteMessaging"
        if (type and ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE != 0) parts += "specialUse"
        return if (parts.isEmpty()) "0x${type.toString(16)}" else parts.joinToString("|")
    }

    private fun acquireWakeLock() {
        if (wakeLock?.isHeld == true) return
        val pm = getSystemService(POWER_SERVICE) as? PowerManager ?: return
        val lock = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "haoma:fgs")
        try {
            lock.setReferenceCounted(false)
            lock.acquire()
            wakeLock = lock
            Logger.i("fgs", "wakelock acquired")
        } catch (t: Throwable) {
            Logger.e("fgs", "wakelock acquire failed", t)
        }
    }

    private fun releaseWakeLock() {
        val lock = wakeLock ?: return
        wakeLock = null
        try {
            if (lock.isHeld) lock.release()
            Logger.i("fgs", "wakelock released")
        } catch (t: Throwable) {
            Logger.w("fgs", "wakelock release failed: ${t.message}")
        }
    }

    private fun registerScreenOffReceiver() {
        if (screenOffReceiver != null) return
        val receiver = object : BroadcastReceiver() {
            override fun onReceive(context: Context?, intent: Intent?) {
                if (intent?.action != Intent.ACTION_SCREEN_OFF) return
                Logger.i("fgs", "ACTION_SCREEN_OFF — firing idle-lock")
                (application as? HaomaApp)?.idleLockDispatcher?.fire("screen-off")
            }
        }
        try {
            registerReceiver(receiver, IntentFilter(Intent.ACTION_SCREEN_OFF))
            screenOffReceiver = receiver
        } catch (t: Throwable) {
            Logger.e("fgs", "registerScreenOffReceiver failed", t)
        }
    }

    private fun unregisterScreenOffReceiver() {
        val receiver = screenOffReceiver ?: return
        try {
            unregisterReceiver(receiver)
        } catch (t: Throwable) {
            Logger.e("fgs", "unregisterScreenOffReceiver failed", t)
        }
        screenOffReceiver = null
    }

    private fun ensureChannel() {
        val mgr = getSystemService(NotificationManager::class.java) ?: return
        if (mgr.getNotificationChannel(CHANNEL_ID) != null) return
        val channel = NotificationChannel(
            CHANNEL_ID,
            getString(R.string.fgs_core_channel_name),
            NotificationManager.IMPORTANCE_LOW,
        ).apply {
            description = getString(R.string.fgs_core_channel_description)
            setShowBadge(false)
        }
        mgr.createNotificationChannel(channel)
    }

    companion object {
        private const val CHANNEL_ID = "haoma_core"
        private const val NOTIF_ID = 1001
        private const val READY_TIMEOUT_MS = 30_000L
        private const val STOP_GRACE_MS = 5_000L

        
        const val ACTION_REFRESH_TYPE = "io.haoma.calculator.fgs.REFRESH_TYPE"

        
        const val ACTION_RESTART_DAEMONS = "io.haoma.calculator.fgs.RESTART_DAEMONS"

        
        fun start(context: Context) {
            val intent = Intent(context, HaomaCoreService::class.java)
            context.startForegroundService(intent)
        }

        
        fun restartDaemons(context: Context) {
            val app = context.applicationContext as? HaomaApp
            if (app == null) {
                Logger.w("fgs", "restartDaemons skipped — applicationContext is not HaomaApp")
                return
            }
            val vault = app.vaultSession
            if (vault == null) {
                Logger.w("fgs", "restartDaemons skipped — no vault session (hard-locked?)")
                return
            }
            val secrets = try {
                vault.secretsForRestart(context)
            } catch (t: Throwable) {
                Logger.e("fgs", "restartDaemons: secretsForRestart failed", t)
                return
            }
            BootstrapPayload.deposit(secrets)
            val intent = Intent(context, HaomaCoreService::class.java)
                .setAction(ACTION_RESTART_DAEMONS)
            try {
                context.startForegroundService(intent)
                Logger.i("fgs", "restartDaemons: ACTION_RESTART_DAEMONS dispatched")
            } catch (t: Throwable) {
                Logger.w("fgs", "restartDaemons dispatch failed: ${t.message}")
            }
        }

        
        fun refreshType(context: Context) {
            val intent = Intent(context, HaomaCoreService::class.java)
                .setAction(ACTION_REFRESH_TYPE)
            try {
                context.startForegroundService(intent)
            } catch (t: Throwable) {
                Logger.w("fgs", "refreshType skipped: ${t.message}")
            }
        }

        fun stop(context: Context) {
            val intent = Intent(context, HaomaCoreService::class.java)
            context.stopService(intent)
        }
    }
}
