package io.haoma.calculator

import android.Manifest
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.provider.Settings
import android.view.MotionEvent
import android.view.WindowManager
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.core.content.ContextCompat
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.lifecycleScope
import io.haoma.calculator.core.UnlockManager
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.MessengerScaffold
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.notifications.NotificationPoster
import io.haoma.calculator.unlock.PassphraseScreen
import io.haoma.calculator.unlock.PatternConfig
import io.haoma.calculator.unlock.VerifyingRevealController
import io.haoma.disguise.AppState
import io.haoma.disguise.DisguiseSkin
import io.haoma.disguise.DisguiseTip
import io.haoma.disguise.RevealController
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.launchIn
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.onEach


class MainActivity : ComponentActivity() {

    
    private val notifPermLauncher = registerForActivityResult(
        ActivityResultContracts.RequestPermission(),
    ) { granted ->
        Logger.i("notifications", "POST_NOTIFICATIONS grant=$granted")
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        Logger.i("activity", "MainActivity.onCreate")
        applySecureWindow()
        requestBatteryOptimizationExemption()

        val app = application as HaomaApp

        
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            app.appState.state
                .map { it == AppState.Warm }
                .distinctUntilChanged()
                .onEach { warm ->
                    if (!warm) return@onEach
                    val granted = ContextCompat.checkSelfPermission(
                        this@MainActivity,
                        Manifest.permission.POST_NOTIFICATIONS,
                    ) == PackageManager.PERMISSION_GRANTED
                    if (!granted) {
                        Logger.i("notifications", "requesting POST_NOTIFICATIONS")
                        notifPermLauncher.launch(Manifest.permission.POST_NOTIFICATIONS)
                    }
                }
                .launchIn(lifecycleScope)
        }

        val skin: DisguiseSkin = app.disguiseSkin
        val reveal: RevealController = VerifyingRevealController(
            state = app.appState,
            config = PatternConfig(disguise = app.disguiseStore),
            unlock = app.unlockManager,
        ) { msg ->
            Logger.i("reveal", msg)
        }

        
        consumeDisguiseTipExtras(intent, app)

        setContent {
            MaterialTheme {
                val state by app.appState.state.collectAsStateWithLifecycle()
                val pendingTip by app.pendingDisguiseTip.collectAsStateWithLifecycle()
                Surface(
                    state = state,
                    skin = skin,
                    reveal = reveal,
                    unlock = app.unlockManager,
                    messenger = app.messengerStore,
                    pendingTip = pendingTip,
                    onTipDismissed = { app.setPendingDisguiseTip(null) },
                )
            }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        consumeDisguiseTipExtras(intent, application as HaomaApp)
    }

    
    private fun consumeDisguiseTipExtras(src: Intent?, app: HaomaApp) {
        val title = src?.getStringExtra(NotificationPoster.EXTRA_DISGUISE_TIP_TITLE)
        val body = src?.getStringExtra(NotificationPoster.EXTRA_DISGUISE_TIP_BODY)
        if (title.isNullOrEmpty() || body.isNullOrEmpty()) return
        Logger.i("notifications", "disguise tip extras consumed")
        app.setPendingDisguiseTip(DisguiseTip(title, body))
    }

    
    override fun dispatchTouchEvent(ev: MotionEvent?): Boolean {
        (application as HaomaApp).idleTimer.touch()
        return super.dispatchTouchEvent(ev)
    }

    
    private fun applySecureWindow() {
        window.setFlags(
            WindowManager.LayoutParams.FLAG_SECURE,
            WindowManager.LayoutParams.FLAG_SECURE,
        )
    }

    
    private fun requestBatteryOptimizationExemption() {
        val pm = getSystemService(Context.POWER_SERVICE) as PowerManager
        if (pm.isIgnoringBatteryOptimizations(packageName)) {
            Logger.i("battery", "already ignoring battery optimizations")
            return
        }
        Logger.i("battery", "requesting battery optimization exemption")
        val intent = Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS)
            .setData(Uri.parse("package:$packageName"))
        try {
            startActivity(intent)
        } catch (t: Throwable) {
            Logger.e("battery", "failed to launch battery optimization request", t)
        }
    }
}

@Composable
private fun Surface(
    state: AppState,
    skin: DisguiseSkin,
    reveal: RevealController,
    unlock: UnlockManager,
    messenger: MessengerStore,
    pendingTip: DisguiseTip?,
    onTipDismissed: () -> Unit,
) {
    
    
    Box(
        modifier = Modifier
            .fillMaxSize()
            .background(Color(0xFF1D2021))
            .safeDrawingPadding(),
    ) {
        when (state) {
            AppState.Locked.Soft,
            AppState.Locked.Safe,
            AppState.Locked.Hard -> skin.Surface(
                reveal = reveal,
                
                
                pendingTip = pendingTip,
                onTipDismissed = onTipDismissed,
            )
            AppState.Locked.Passphrase -> PassphraseScreen(
                unlock = unlock,
                log = { Logger.i("passphrase", it) },
            )
            AppState.Warm -> MessengerScaffold(store = messenger)
        }
    }
}

