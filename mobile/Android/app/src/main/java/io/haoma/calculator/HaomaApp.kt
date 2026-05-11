package io.haoma.calculator

import android.app.Application
import android.app.KeyguardManager
import android.content.Context
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

class HaomaApp : Application(), ImageLoaderFactory {
    

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

    
    lateinit var disguiseSkin: DisguiseSkin
        private set

    
    private val _pendingDisguiseTip = MutableStateFlow<DisguiseTip?>(null)
    val pendingDisguiseTip: StateFlow<DisguiseTip?> = _pendingDisguiseTip.asStateFlow()

    fun setPendingDisguiseTip(tip: DisguiseTip?) {
        _pendingDisguiseTip.value = tip
    }

    @Volatile
    private var idlePolicy: IdlePolicy? = null

    
    private val appScope = CoroutineScope(SupervisorJob() + Dispatchers.Default)

    
    @Volatile
    var vaultSession: VaultSession? = null
        private set

    override fun onCreate() {
        super.onCreate()
        Logger.init(this, BuildConfig.DEBUG)
        val previous = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { thread, throwable ->
            Logger.e("uncaught", "thread=${thread.name}", throwable)
            previous?.uncaughtException(thread, throwable)
        }
        Logger.i("app", "HaomaApp.onCreate debug=${BuildConfig.DEBUG}")

        appState = AppStateRepository()
        disguiseStore = DisguiseStore(applicationContext)
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
            },
        )
        idleLockDispatcher = IdleLockDispatcher(
            state = appState,
            policySource = { idlePolicy },
            stopFgs = {
                HaomaCoreService.stop(applicationContext)
                idlePolicy = null
                vaultSession = null
            },
        )
        idleTimer = ForegroundIdleTimer(
            state = appState,
            policySource = { idlePolicy },
            dispatcher = idleLockDispatcher,
        )
        disguiseSkin = CalculatorSkin()
        
        
        notificationPoster = NotificationPoster(
            app = applicationContext,
            settingsProvider = { messengerStore.loadNotificationSettings() },
            tipProvider = { disguiseSkin.nextTip() },
        )
        messengerStore = MessengerStore(
            clientName = "haoma-android",
            clientVersion = BuildConfig.VERSION_NAME,
            vaultSessionProvider = { vaultSession },
            disguise = disguiseStore,
            notificationPoster = notificationPoster,
            appContext = applicationContext,
        )

        
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
                
                
                if (!softLocked) setPendingDisguiseTip(null)
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
        }

        override fun onStop(owner: LifecycleOwner) {
            idleTimer.pause()
            
            
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
