package io.haoma.calculator.notifications

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import io.haoma.calculator.HaomaApp
import io.haoma.calculator.MainActivity
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.CallAction
import io.haoma.calculator.messenger.respondCall


class CallActionReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val callId = intent.getStringExtra(NotificationPoster.EXTRA_CALL_ID).orEmpty()
        if (callId.isEmpty()) {
            Logger.w("notifications", "call-action receive: missing call_id, dropping")
            return
        }
        val app = context.applicationContext as? HaomaApp ?: run {
            Logger.w("notifications", "call-action receive: app context cast failed")
            return
        }
        when (intent.action) {
            NotificationPoster.ACTION_ANSWER -> {
                Logger.i("notifications", "call-action ANSWER call=${callId.take(8)}…")
                app.messengerStore.respondCall(callId, CallAction.Accept)
                app.notificationPoster.cancelCall(callId)
                
                
                val launch = Intent(context, MainActivity::class.java).apply {
                    addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP)
                }
                context.startActivity(launch)
            }
            NotificationPoster.ACTION_DECLINE -> {
                Logger.i("notifications", "call-action DECLINE call=${callId.take(8)}…")
                app.messengerStore.respondCall(callId, CallAction.Reject)
                app.notificationPoster.cancelCall(callId)
            }
            else -> Logger.w("notifications", "call-action receive: unknown action ${intent.action}")
        }
    }
}
