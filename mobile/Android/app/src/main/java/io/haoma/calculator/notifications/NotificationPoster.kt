package io.haoma.calculator.notifications

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import io.haoma.calculator.MainActivity
import io.haoma.calculator.R
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.NotificationEmittedPayload
import io.haoma.calculator.messenger.NotificationSettings
import io.haoma.disguise.DisguiseTip


class NotificationPoster(
    private val app: Context,
    private val settingsProvider: () -> NotificationSettings? = { null },
    private val tipProvider: () -> DisguiseTip = { DisguiseTip("Math Tip", "New message") },
) {

    init {
        ensureChannel()
    }

    
    fun post(payload: NotificationEmittedPayload) {
        val mgr = NotificationManagerCompat.from(app)
        if (!mgr.areNotificationsEnabled()) {
            Logger.i(
                "notifications",
                "skip reason=permission-denied chat=${shortTag(payload.chatId)}",
            )
            return
        }

        val settings = settingsProvider()
        val disguiseActive = settings?.disguiseEnabled == true &&
            payload.redactedSender &&
            payload.redactedBody

        val (title, body) = if (disguiseActive) {
            val tip = tipProvider()
            tip.title to tip.body
        } else {
            payload.title to payload.body
        }

        val tapIntent = Intent(app, MainActivity::class.java).apply {
            addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP)
            if (disguiseActive) {
                
                
                putExtra(EXTRA_DISGUISE_TIP_TITLE, title)
                putExtra(EXTRA_DISGUISE_TIP_BODY, body)
            }
        }
        val tap = PendingIntent.getActivity(
            app,
            
            
            if (disguiseActive) 1 else 0,
            tapIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )

        val notification = NotificationCompat.Builder(app, CHANNEL_ID)
            
            
            .setSmallIcon(android.R.drawable.ic_menu_info_details)
            .setContentTitle(title)
            .setContentText(body)
            .setStyle(NotificationCompat.BigTextStyle().bigText(body))
            .setVisibility(NotificationCompat.VISIBILITY_SECRET)
            .setCategory(NotificationCompat.CATEGORY_MESSAGE)
            .setAutoCancel(true)
            .setContentIntent(tap)
            .setShowWhen(true)
            .build()
        try {
            mgr.notify(payload.chatId, NOTIF_ID_MESSAGE, notification)
            Logger.i(
                "notifications",
                "posted chat=${shortTag(payload.chatId)} disguise=$disguiseActive",
            )
        } catch (t: SecurityException) {
            
            
            Logger.w("notifications", "notify() threw: ${t.message}")
        }
    }

    
    fun cancelAll() {
        val mgr = NotificationManagerCompat.from(app)
        
        
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            val sysMgr = app.getSystemService(NotificationManager::class.java) ?: return
            sysMgr.activeNotifications
                .filter { it.id == NOTIF_ID_MESSAGE }
                .forEach { mgr.cancel(it.tag, it.id) }
            Logger.i("notifications", "cancelAll cleared message banners")
            return
        }
        
        
    }

    private fun ensureChannel() {
        val mgr = app.getSystemService(NotificationManager::class.java) ?: return
        if (mgr.getNotificationChannel(CHANNEL_ID) == null) {
            val channel = NotificationChannel(
                CHANNEL_ID,
                app.getString(R.string.messages_channel_name),
                NotificationManager.IMPORTANCE_DEFAULT,
            ).apply {
                description = app.getString(R.string.messages_channel_description)
                lockscreenVisibility = NotificationCompat.VISIBILITY_SECRET
                setShowBadge(false)
                
            }
            mgr.createNotificationChannel(channel)
        }
        if (mgr.getNotificationChannel(CALLS_CHANNEL_ID) == null) {
            val calls = NotificationChannel(
                CALLS_CHANNEL_ID,
                app.getString(R.string.calls_channel_name),
                NotificationManager.IMPORTANCE_HIGH,
            ).apply {
                description = app.getString(R.string.calls_channel_description)
                
                
                lockscreenVisibility = NotificationCompat.VISIBILITY_PRIVATE
                setShowBadge(false)
                enableVibration(true)
                enableLights(true)
            }
            mgr.createNotificationChannel(calls)
        }
    }

    
    fun postCall(
        callId: String,
        chatId: String,
        peerLabel: String,
        softLocked: Boolean,
    ) {
        val mgr = NotificationManagerCompat.from(app)
        if (!mgr.areNotificationsEnabled()) {
            Logger.i("notifications", "skip call notify reason=permission-denied call=${shortTag(callId)}")
            return
        }
        if (softLocked && settingsProvider()?.onLock != true) {
            Logger.i(
                "notifications",
                "skip call notify reason=soft-locked+notifications_on_lock=false call=${shortTag(callId)}",
            )
            return
        }

        val title = app.getString(R.string.calls_incoming_title)
        val body = app.getString(R.string.calls_incoming_body, peerLabel)

        val tapIntent = Intent(app, MainActivity::class.java).apply {
            addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP)
        }
        val tap = PendingIntent.getActivity(
            app,
            callId.hashCode(),
            tapIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )

        val answer = callActionPendingIntent(callId, ACTION_ANSWER)
        val decline = callActionPendingIntent(callId, ACTION_DECLINE)

        val notification = NotificationCompat.Builder(app, CALLS_CHANNEL_ID)
            .setSmallIcon(android.R.drawable.ic_menu_call)
            .setContentTitle(title)
            .setContentText(body)
            .setStyle(NotificationCompat.BigTextStyle().bigText(body))
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .setCategory(NotificationCompat.CATEGORY_CALL)
            .setVisibility(NotificationCompat.VISIBILITY_PRIVATE)
            .setAutoCancel(false)
            .setOngoing(true)
            .setContentIntent(tap)
            .addAction(0, app.getString(R.string.calls_answer), answer)
            .addAction(0, app.getString(R.string.calls_decline), decline)
            .build()
        try {
            mgr.notify(callId, NOTIF_ID_CALL, notification)
            Logger.i("notifications", "posted call call=${shortTag(callId)} peer=$peerLabel")
        } catch (t: SecurityException) {
            Logger.w("notifications", "call notify() threw: ${t.message}")
        }
    }

    
    fun cancelCall(callId: String) {
        try {
            NotificationManagerCompat.from(app).cancel(callId, NOTIF_ID_CALL)
            Logger.i("notifications", "cancelled call call=${shortTag(callId)}")
        } catch (t: Throwable) {
            Logger.w("notifications", "cancel call notify failed: ${t.message}")
        }
    }

    private fun callActionPendingIntent(callId: String, action: String): PendingIntent {
        val intent = Intent(app, io.haoma.calculator.notifications.CallActionReceiver::class.java).apply {
            this.action = action
            putExtra(EXTRA_CALL_ID, callId)
        }
        
        
        val requestCode = (callId + action).hashCode()
        return PendingIntent.getBroadcast(
            app,
            requestCode,
            intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
    }

    private fun shortTag(tag: String): String =
        if (tag.length <= 8) tag else tag.substring(0, 8) + "…"

    companion object {
        const val CHANNEL_ID = "haoma_messages"
        const val CALLS_CHANNEL_ID = "haoma_calls"
        const val NOTIF_ID_MESSAGE = 2001
        const val NOTIF_ID_CALL = 2002

        
        const val EXTRA_DISGUISE_TIP_TITLE = "haoma.disguise_tip_title"
        const val EXTRA_DISGUISE_TIP_BODY = "haoma.disguise_tip_body"

        
        const val ACTION_ANSWER = "io.haoma.calculator.CALL_ANSWER"
        const val ACTION_DECLINE = "io.haoma.calculator.CALL_DECLINE"
        const val EXTRA_CALL_ID = "haoma.call_id"
    }
}
