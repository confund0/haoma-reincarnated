package io.haoma.calculator.messenger.invites

import android.content.Context


object EffShort {
    @Volatile private var set: Set<String>? = null

    private fun ensureLoaded(context: Context): Set<String> {
        var s = set
        if (s != null) return s
        synchronized(this) {
            s = set
            if (s == null) {
                s = context.applicationContext.assets
                    .open("effshort_words.txt")
                    .bufferedReader()
                    .useLines { seq ->
                        seq.map { it.trim().lowercase() }
                            .filter { it.isNotEmpty() }
                            .toHashSet()
                    }
                set = s
            }
        }
        return s!!
    }

    
    fun looksValid(context: Context, words: List<String>): Boolean {
        if (words.size != 7) return false
        val s = ensureLoaded(context)
        return words.all { it.trim().lowercase() in s }
    }
}
