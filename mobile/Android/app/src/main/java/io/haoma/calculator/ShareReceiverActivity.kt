package io.haoma.calculator

import android.app.Activity
import android.content.ClipData
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import io.haoma.calculator.log.Logger


class ShareReceiverActivity : Activity() {
    @Suppress("DEPRECATION")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val uri: Uri? = when (intent?.action) {
            Intent.ACTION_SEND -> intent.getParcelableExtra(Intent.EXTRA_STREAM)
            Intent.ACTION_SEND_MULTIPLE ->
                intent.getParcelableArrayListExtra<Uri>(Intent.EXTRA_STREAM)?.firstOrNull()
            else -> null
        }
        if (uri == null) {
            Logger.i("share", "share receiver: no stream uri; dropping")
            finish()
            return
        }
        Logger.i("share", "share receiver → MainActivity type=${intent?.type ?: "?"}")
        val fwd = Intent(this, MainActivity::class.java).apply {
            action = Intent.ACTION_SEND
            type = intent?.type
            putExtra(Intent.EXTRA_STREAM, uri)
            
            
            clipData = ClipData.newRawUri(null, uri)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP)
        }
        startActivity(fwd)
        finish()
    }
}
