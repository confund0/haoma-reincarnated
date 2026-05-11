package io.haoma.calculator.core

import org.json.JSONObject


data class IdlePolicy(
    val action: String,
    val timeoutSeconds: Int,
) {
    companion object {
        const val Soft = "soft-lock"
        const val Safe = "safe-lock"
        const val Hard = "hard-lock"

        const val DefaultTimeoutSeconds = 1800

        val Default = IdlePolicy(action = Safe, timeoutSeconds = DefaultTimeoutSeconds)

        fun fromJson(o: JSONObject): IdlePolicy {
            val rawAction = o.optString("idle_action", "")
            val action = when (rawAction) {
                Soft, Safe, Hard -> rawAction
                "" -> Safe
                else -> Safe
            }
            val rawTimeout = o.optInt("idle_timeout_seconds", DefaultTimeoutSeconds)
            val timeout = if (rawTimeout > 0) rawTimeout else DefaultTimeoutSeconds
            return IdlePolicy(action = action, timeoutSeconds = timeout)
        }
    }
}
