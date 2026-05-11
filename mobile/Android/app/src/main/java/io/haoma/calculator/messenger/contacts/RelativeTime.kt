package io.haoma.calculator.messenger.contacts


object RelativeTime {
    private const val MIN_SECONDS = 60L
    private const val HOUR_SECONDS = 60L * MIN_SECONDS
    private const val DAY_SECONDS = 24L * HOUR_SECONDS
    private const val MONTH_SECONDS = 30L * DAY_SECONDS
    private const val YEAR_SECONDS = 365L * DAY_SECONDS

    
    fun format(nowSeconds: Long, tsSeconds: Long): String {
        if (tsSeconds <= 0L) return "—"
        val diff = nowSeconds - tsSeconds
        if (diff <= 0L) return "now"
        return when {
            diff < MIN_SECONDS -> "now"
            diff < HOUR_SECONDS -> "${diff / MIN_SECONDS}min"
            diff < DAY_SECONDS -> "${diff / HOUR_SECONDS}h"
            diff < MONTH_SECONDS -> {
                val days = diff / DAY_SECONDS
                val hours = (diff % DAY_SECONDS) / HOUR_SECONDS
                "${days}d${hours}h"
            }
            diff < YEAR_SECONDS -> {
                val months = diff / MONTH_SECONDS
                val days = (diff % MONTH_SECONDS) / DAY_SECONDS
                "${months}m${days}d"
            }
            else -> {
                val years = diff / YEAR_SECONDS
                val months = (diff % YEAR_SECONDS) / MONTH_SECONDS
                "${years}y${months}m"
            }
        }
    }
}
