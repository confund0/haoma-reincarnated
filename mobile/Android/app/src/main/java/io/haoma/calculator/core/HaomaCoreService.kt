package io.haoma.calculator.core

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import io.haoma.calculator.HaomaApp
import io.haoma.calculator.R
import io.haoma.calculator.log.Logger
import java.io.File
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

    
    private var screenOffReceiver: BroadcastReceiver? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        Logger.i("fgs", "HaomaCoreService.onCreate")
        ensureChannel()
        registerScreenOffReceiver()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        Logger.i("fgs", "HaomaCoreService.onStartCommand startId=$startId flags=$flags")
        startForegroundCompat()
        if (haomad == null) {
            scope.launch { bootstrapFromPayload() }
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
        haomad?.let { d ->
            try {
                d.stop(STOP_GRACE_MS)
            } catch (t: Throwable) {
                Logger.e("fgs", "haomad stop", t)
            }
        }
        haomad = null
        scope.cancel()
        super.onDestroy()
    }

    private fun bootstrapFromPayload() {
        val claimed = BootstrapPayload.take()
        if (claimed == null) {
            Logger.w("fgs", "onStartCommand without a deposited payload; stopping")
            stopSelf()
            return
        }
        val (secrets, ack) = claimed
        try {
            val haomadDaemon = spawnHaomad(secrets)
            haomad = haomadDaemon
            val haomadAddr = haomadDaemon.waitReady(READY_TIMEOUT_MS)
            Logger.i("fgs", "haomad up api_addr=$haomadAddr")

            val haomaDaemon = spawnHaoma(secrets, haomadAddr)
            haoma = haomaDaemon
            val haomaAddr = haomaDaemon.waitReady(READY_TIMEOUT_MS)
            Logger.i("fgs", "haoma up api_addr=$haomaAddr")

            attachMessengerStore(haomaAddr)

            ack.complete(BootstrapPayload.Result.Ok(haomadAddr, haomaAddr))
        } catch (t: Throwable) {
            Logger.e("fgs", "daemon bootstrap failed", t)
            ack.complete(BootstrapPayload.Result.Fail(t.message ?: t.javaClass.simpleName))
            runCatching { (application as? HaomaApp)?.messengerStore?.onDaemonsStopped() }
            haoma?.let { runCatching { it.stop(STOP_GRACE_MS) } }
            haoma = null
            haomad?.let { runCatching { it.stop(STOP_GRACE_MS) } }
            haomad = null
            stopSelf()
        } finally {
            
            
            secrets.fill(0)
        }
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
            startForeground(
                NOTIF_ID,
                notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_REMOTE_MESSAGING,
            )
        } else {
            startForeground(NOTIF_ID, notification)
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

        
        fun start(context: Context) {
            val intent = Intent(context, HaomaCoreService::class.java)
            context.startForegroundService(intent)
        }

        fun stop(context: Context) {
            val intent = Intent(context, HaomaCoreService::class.java)
            context.stopService(intent)
        }
    }
}
