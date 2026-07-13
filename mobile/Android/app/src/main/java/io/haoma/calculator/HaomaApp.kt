package io.haoma.calculator

import android.app.Application
import android.app.KeyguardManager
import android.content.Context
import android.net.Uri
import android.os.PowerManager
import androidx.lifecycle.DefaultLifecycleObserver
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.ProcessLifecycleOwner
import coil.ImageLoader
import coil.ImageLoaderFactory
import coil.imageLoader
import coil.request.CachePolicy
import io.haoma.calculator.core.DisguiseStore
import io.haoma.calculator.core.ForegroundIdleTimer
import io.haoma.calculator.core.HaomaCoreService
import io.haoma.calculator.core.IdleLockDispatcher
import io.haoma.calculator.core.IdlePolicy
import io.haoma.calculator.core.UnlockKeysStore
import io.haoma.calculator.core.UnlockManager
import io.haoma.calculator.core.VaultSession
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.*
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.notifications.NotificationPoster
import io.haoma.calculator.unlock.PatternConfig
import io.haoma.disguise.AppState
import io.haoma.disguise.AppStateRepository
import io.haoma.disguise.DisguiseSkin
import io.haoma.disguise.DisguiseTip
import io.haoma.disguise.calculator.CalculatorSkin
import io.haoma.disguise.calculator.RevealConfig
import kotlin.concurrent.thread
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.filterNotNull
import kotlinx.coroutines.flow.launchIn
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.onEach
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeoutOrNull

private const val HARD_LOCK_HANGUP_GRACE_MS = 200L


data class ShareAttach(val chatId: String, val uri: Uri)

class HaomaApp : Application(), ImageLoaderFactory {
    companion object {
        

        val PROCESS_STARTED_MS: Long = System.currentTimeMillis()
    }

    
    override fun newImageLoader(): ImageLoader = ImageLoader.Builder(this)
        .diskCachePolicy(CachePolicy.DISABLED)
        .respectCacheHeaders(false)
        .build()

    
    lateinit var appState: AppStateRepository
        private set

    lateinit var unlockManager: UnlockManager
        private set

    
    lateinit var idleLockDispatcher: IdleLockDispatcher
        private set

    
    lateinit var idleTimer: ForegroundIdleTimer
        private set

    
    lateinit var disguiseStore: DisguiseStore
        private set

    
    lateinit var messengerStore: MessengerStore
        private set

    
    lateinit var notificationPoster: NotificationPoster
        private set

    
    val deepLinkChatId = MutableStateFlow<String?>(null)

    
    val pendingShareUri = MutableStateFlow<Uri?>(null)

    
    val pendingShareAttach = MutableStateFlow<ShareAttach?>(null)

    
    lateinit var proximityController: io.haoma.calculator.messenger.calls.ProximityController
        private set

    
    lateinit var disguiseSkin: DisguiseSkin
        private set

    
    private val revealConfigFlow = MutableStateFlow(RevealConfig())

    
    lateinit var unlockKeysStore: UnlockKeysStore
        private set

    
    private val _pendingDisguiseTip = MutableStateFlow<DisguiseTip?>(null)
    val pendingDisguiseTip: StateFlow<DisguiseTip?> = _pendingDisguiseTip.asStateFlow()

    fun setPendingDisguiseTip(tip: DisguiseTip?) {
        _pendingDisguiseTip.value = tip
    }

    
    private val _stagedRestore = MutableStateFlow<io.haoma.calculator.core.StagedRestoreState?>(null)
    val stagedRestore: StateFlow<io.haoma.calculator.core.StagedRestoreState?> = _stagedRestore.asStateFlow()

    fun setStagedRestore(state: io.haoma.calculator.core.StagedRestoreState?) {
        _stagedRestore.value = state
    }

    
    fun launchStagedRestore(
        archiveUri: android.net.Uri,
        log: (String) -> Unit,
        onError: (String) -> Unit,
        onComplete: () -> Unit,
    ) {
        appScope.launch {
            val outcome = withContext(Dispatchers.IO) {
                runCatching {
                    val cacheFile = java.io.File(
                        applicationContext.cacheDir,
                        "restore-${System.currentTimeMillis()}.tar.zst",
                    )
                    try {
                        contentResolver.openInputStream(archiveUri).use { input ->
                            requireNotNull(input) { "openInputStream returned null" }
                            cacheFile.outputStream().use { out -> input.copyTo(out) }
                        }
                        io.haoma.calculator.core.VaultHelper.archiveStage(
                            applicationContext,
                            cacheFile.absolutePath,
                        )
                    } finally {
                        if (cacheFile.exists() && !cacheFile.delete()) {
                            log("stage cache slot survived deletion: ${cacheFile.absolutePath}")
                        }
                    }
                }
            }
            withContext(Dispatchers.Main) {
                outcome.onSuccess { stagingPath ->
                    log("stage ok staging=$stagingPath")
                    setStagedRestore(io.haoma.calculator.core.StagedRestoreState(stagingPath = stagingPath))
                }.onFailure { t ->
                    log("stage failed: ${t.message}")
                    val msg = "Restore failed: ${t.message ?: "unknown error"}"
                    android.widget.Toast.makeText(applicationContext, msg, android.widget.Toast.LENGTH_LONG).show()
                    onError(msg)
                }
                onComplete()
            }
        }
    }

    
    private fun seedStagedRestoreFromDisk() {
        appScope.launch {
            val staged = withContext(Dispatchers.IO) {
                runCatching { io.haoma.calculator.core.VaultHelper.findStagedRestoreDirs(applicationContext) }
                    .getOrElse {
                        Logger.w("app", "staged-restore seed scan failed: ${it.message}")
                        emptyList()
                    }
            }
            if (staged.isEmpty()) return@launch
            Logger.i("app", "staged-restore seeded from disk (${staged.size} dir(s); newest=${staged.first()})")
            withContext(Dispatchers.Main) {
                setStagedRestore(io.haoma.calculator.core.StagedRestoreState(stagingPath = staged.first()))
            }
        }
    }

    
    fun launchStagedRestoreDiscard(
        stagingPath: String,
        log: (String) -> Unit,
        onComplete: () -> Unit,
    ) {
        appScope.launch {
            val result = withContext(Dispatchers.IO) {
                runCatching { io.haoma.calculator.core.VaultHelper.archiveDiscard(applicationContext, stagingPath) }
            }
            result.onSuccess {
                log("discard ok")
            }.onFailure { t ->
                log("discard failed (clearing in-memory anyway): ${t.message}")
            }
            withContext(Dispatchers.Main) {
                setStagedRestore(null)
                onComplete()
            }
        }
    }

    @Volatile
    private var idlePolicy: IdlePolicy? = null

    
    private val appScope = CoroutineScope(SupervisorJob() + Dispatchers.Default)

    
    internal val hangupScope = CoroutineScope(SupervisorJob() + Dispatchers.Default)

    
    @Volatile
    var vaultSession: VaultSession? = null
        private set

    override fun onCreate() {
        super.onCreate()
        Logger.init(this, BuildConfig.DEBUG)
        
        
        Thread.setDefaultUncaughtExceptionHandler { thread, throwable ->
            Logger.e("uncaught", "thread=${thread.name}", throwable)
            android.os.Process.killProcess(android.os.Process.myPid())
        }
        Logger.i("app", "HaomaApp.onCreate debug=${BuildConfig.DEBUG}")

        appState = AppStateRepository()
        seedStagedRestoreFromDisk()
        disguiseStore = DisguiseStore(applicationContext)
        unlockKeysStore = UnlockKeysStore(applicationContext)
        
        
        unlockKeysStore.load().let { keys ->
            revealConfigFlow.value = RevealConfig(
                triggerKey = keys.patternKey,
                pinTriggerKey = keys.pinKey,
                bypassKey = keys.bypassKey,
            )
            Logger.i(
                "app",
                "reveal keys seeded pattern=${keys.patternKey} pin=${keys.pinKey} " +
                    "bypass=${keys.bypassKey.ifEmpty { "(disabled)" }}",
            )
        }
        unlockManager = UnlockManager(
            app = applicationContext,
            state = appState,
            policySink = { policy ->
                idlePolicy = policy
                Logger.i("app", "idle policy installed action=${policy.action} t=${policy.timeoutSeconds}s")
            },
            sessionSink = { session ->
                vaultSession = session
                Logger.i("app", "vault session installed=${session != null}")
                
                
                if (this::messengerStore.isInitialized && session != null) {
                    messengerStore.loadNoticeSnoozeFromVault()
                    
                    
                    messengerStore.reconcileShareTarget(session)
                }
            },
            passphraseDefaultSink = { isDefault ->
                if (this::messengerStore.isInitialized) {
                    messengerStore.setPassphraseIsDefault(isDefault)
                }
            },
        )
        idleLockDispatcher = IdleLockDispatcher(
            state = appState,
            policySource = { idlePolicy },
            stopFgs = {
                HaomaCoreService.stop(applicationContext)
                idlePolicy = null
                
                
                val expiring = vaultSession
                vaultSession = null
                expiring?.wipe()
            },
            
            
            stopHaomaOnly = {
                HaomaCoreService.stopHaomaOnly(applicationContext)
                idlePolicy = null
                
                
                val expiring = vaultSession
                vaultSession = null
                expiring?.wipe()
            },
            
            
            onWillLock = { action ->
                val job = messengerStore.hangupAllActive(hangupScope)
                if (action == IdlePolicy.Hard) {
                    runBlocking {
                        withTimeoutOrNull(HARD_LOCK_HANGUP_GRACE_MS) { job.join() }
                    }
                }
            },
        )
        idleTimer = ForegroundIdleTimer(
            state = appState,
            policySource = { idlePolicy },
            dispatcher = idleLockDispatcher,
        )
        disguiseSkin = CalculatorSkin(configFlow = revealConfigFlow.asStateFlow())
        
        
        notificationPoster = NotificationPoster(
            app = applicationContext,
            settingsProvider = { messengerStore.loadNotificationSettings() },
            tipProvider = { disguiseSkin.nextTip() },
            iconProvider = { disguiseSkin.notificationIconRes },
            
            
            isForegroundedProvider = { appState.state.value is AppState.Warm },
            isKeyguardLockedProvider = {
                val km = getSystemService(Context.KEYGUARD_SERVICE) as? KeyguardManager
                km?.isKeyguardLocked == true
            },
        )
        messengerStore = MessengerStore(
            clientName = "haoma-android",
            clientVersion = BuildConfig.VERSION_NAME,
            vaultSessionProvider = { vaultSession },
            disguise = disguiseStore,
            notificationPoster = notificationPoster,
            appContext = applicationContext,
            policyUpdater = { policy ->
                idlePolicy = policy
                Logger.i("app", "idle policy refreshed action=${policy.action} t=${policy.timeoutSeconds}s")
            },
            unlockKeysStore = unlockKeysStore,
            revealKeysUpdater = { keys ->
                
                
                revealConfigFlow.value = RevealConfig(
                    triggerKey = keys.patternKey,
                    pinTriggerKey = keys.pinKey,
                    bypassKey = keys.bypassKey,
                )
                Logger.i(
                    "app",
                    "reveal keys refreshed pattern=${keys.patternKey} " +
                        "pin=${keys.pinKey} bypass=${keys.bypassKey.ifEmpty { "(disabled)" }}",
                )
            },
        )
        
        
        val audioRouter = io.haoma.calculator.messenger.calls.AudioRouter(
            app = applicationContext,
            activeCallsSource = messengerStore.activeCalls,
            bluetoothConnectGrantedSource = messengerStore.bluetoothConnectGranted,
        ).also { it.start() }
        messengerStore.audioRouter = audioRouter

        
        proximityController = io.haoma.calculator.messenger.calls.ProximityController(
            app = applicationContext,
            activeCallsSource = messengerStore.activeCalls,
        ).also { it.start() }

        
        audioRouter.inCallActive
            .onEach { idleTimer.setInCall(it) }
            .launchIn(appScope)

        
        thread(name = "disguise-bootstrap", isDaemon = true) {
            try {
                disguiseStore.bootstrapIfMissing(PatternConfig.FACTORY_DEFAULT)
            } catch (t: Throwable) {
                
                
                Logger.w("app", "disguise bootstrap failed: ${t.message}")
            }
        }

        ProcessLifecycleOwner.get().lifecycle.addObserver(processObserver)

        
        appState.state
            .map { s ->
                when (s) {
                    AppState.Locked.Soft, AppState.Locked.Safe -> true
                    AppState.Warm -> false
                    else -> null
                }
            }
            .filterNotNull()
            .distinctUntilChanged()
            .onEach { softLocked ->
                messengerStore.syncLockState(softLocked)
                
                
                applicationContext.imageLoader.memoryCache?.clear()
                
                
                if (!softLocked) {
                    setPendingDisguiseTip(null)
                    
                    
                    notificationPoster.enrollAllActiveOnUnlock()
                }
            }
            .launchIn(appScope)
    }

    
    private val processObserver = object : DefaultLifecycleObserver {
        override fun onStart(owner: LifecycleOwner) {
            Logger.i("app", "process ON_START")
            
            
            val keyguard = getSystemService(Context.KEYGUARD_SERVICE) as? KeyguardManager
            if (keyguard?.isKeyguardLocked == true) {
                Logger.i("app", "ON_START with keyguard still locked — firing idle-lock")
                idleLockDispatcher.fire("on-start-keyguard-locked")
            }
            idleTimer.resume()
            
            
            messengerStore.refireFocusOnResume()
            
            
            messengerStore.requestExternalProbeBurst()
            messengerStore.requestSelfProbeForActiveSurface()
        }

        override fun onStop(owner: LifecycleOwner) {
            idleTimer.pause()
            
            
            deepLinkChatId.value = null
            
            
            pendingShareUri.value = null
            pendingShareAttach.value = null
            
            
            messengerStore.pauseFocusOnBackground()
            val power = getSystemService(Context.POWER_SERVICE) as? PowerManager
            val keyguard = getSystemService(Context.KEYGUARD_SERVICE) as? KeyguardManager
            val screenOff = power?.isInteractive == false
            val keyguardUp = keyguard?.isKeyguardLocked == true
            if (screenOff || keyguardUp) {
                Logger.i(
                    "app",
                    "process ON_STOP screenOff=$screenOff keyguardUp=$keyguardUp — firing idle-lock",
                )
                idleLockDispatcher.fire("device-locked")
            } else {
                Logger.i("app", "process ON_STOP — task-switch (screen on, no keyguard); not firing")
            }
        }
    }
}
