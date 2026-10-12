package io.haoma.calculator.core

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import io.haoma.calculator.log.Logger


class DefaultNetworkTracker(initial: Long?) {
    private var current: Long? = initial

    
    @Synchronized
    fun onAvailable(handle: Long): Boolean {
        val changed = current != handle
        current = handle
        return changed
    }
}


class NetworkChangeWatcher(
    private val context: Context,
    private val onChange: () -> Unit,
) {
    private var callback: ConnectivityManager.NetworkCallback? = null

    fun start() {
        if (callback != null) return
        val cm = context.getSystemService(ConnectivityManager::class.java) ?: run {
            Logger.w(TAG, "no ConnectivityManager; network changes are not reported")
            return
        }
        
        
        val tracker = DefaultNetworkTracker(cm.activeNetwork?.networkHandle)
        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                if (tracker.onAvailable(network.networkHandle)) {
                    Logger.i(TAG, "default network changed to another network")
                    onChange()
                }
            }
        }
        try {
            cm.registerDefaultNetworkCallback(cb)
            callback = cb
        } catch (t: Throwable) {
            Logger.e(TAG, "registerDefaultNetworkCallback failed", t)
        }
    }

    fun stop() {
        val cb = callback ?: return
        callback = null
        try {
            context.getSystemService(ConnectivityManager::class.java)?.unregisterNetworkCallback(cb)
        } catch (t: Throwable) {
            Logger.e(TAG, "unregisterNetworkCallback failed", t)
        }
    }

    private companion object {
        const val TAG = "fgs.network"
    }
}
