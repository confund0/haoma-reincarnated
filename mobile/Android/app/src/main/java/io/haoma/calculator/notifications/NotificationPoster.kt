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
        if (mgr.getNotificationChannel(CHANNEL_ID) != null) return
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

    private fun shortTag(tag: String): String =
        if (tag.length <= 8) tag else tag.substring(0, 8) + "…"

    companion object {
        const val CHANNEL_ID = "haoma_messages"
        const val NOTIF_ID_MESSAGE = 2001

        
        const val EXTRA_DISGUISE_TIP_TITLE = "haoma.disguise_tip_title"
        const val EXTRA_DISGUISE_TIP_BODY = "haoma.disguise_tip_body"
    }
}
