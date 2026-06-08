package io.haoma.calculator.messenger

import io.haoma.calculator.log.Logger
import kotlinx.coroutines.launch
import org.json.JSONObject


enum class NoticeSeverity { INFO, WARN, SEVERE }


data class Notice(
    val id: String,
    val severity: NoticeSeverity,
    val title: String,
    val body: String,
    val snoozeUntil: Long,
)


data class NoticeSnoozeEntry(val until: Long, val step: Int)


private val SNOOZE_DAYS = listOf(1, 2, 3, 7, 7)
private const val MS_PER_DAY = 24L * 60L * 60L * 1000L


const val NOTICE_PASSPHRASE_IS_DEFAULT = "passphrase_is_default"

internal fun produceNotices(
    passphraseIsDefault: Boolean,
    snooze: Map<String, NoticeSnoozeEntry>,
): List<Notice> {
    val out = ArrayList<Notice>(2)
    if (passphraseIsDefault) {
        out += Notice(
            id = NOTICE_PASSPHRASE_IS_DEFAULT,
            severity = NoticeSeverity.SEVERE,
            title = "Default passphrase in use",
            body = "Your vault opens with the install-shipped passphrase. " +
                "Anyone holding this phone can unlock Haoma. " +
                "Set a custom passphrase in Settings → Lock.",
            snoozeUntil = snooze[NOTICE_PASSPHRASE_IS_DEFAULT]?.until ?: 0L,
        )
    }
    return out
}


fun MessengerStore.setPassphraseIsDefault(isDefault: Boolean) {
    _noticePassphraseIsDefault.value = isDefault
    Logger.i("notices", "passphraseIsDefault=$isDefault")
}


fun MessengerStore.loadNoticeSnoozeFromVault() {
    val session = vaultSessionProvider() ?: run {
        _noticeSnooze.value = emptyMap()
        return
    }
    val snap = session.snapshot()
    val obj = snap.optJSONObject("mobile_notice_snooze") ?: run {
        _noticeSnooze.value = emptyMap()
        return
    }
    val out = HashMap<String, NoticeSnoozeEntry>(obj.length())
    val keys = obj.keys()
    while (keys.hasNext()) {
        val k = keys.next()
        val e = obj.optJSONObject(k) ?: continue
        out[k] = NoticeSnoozeEntry(
            until = e.optLong("until", 0L),
            step = e.optInt("step", 0),
        )
    }
    _noticeSnooze.value = out
}


fun MessengerStore.snoozeNotice(id: String) {
    scope.launch {
        val now = System.currentTimeMillis()
        val prior = _noticeSnooze.value[id]
        val nextStep = ((prior?.step ?: -1) + 1).coerceAtMost(SNOOZE_DAYS.lastIndex)
        val daysAdd = SNOOZE_DAYS[nextStep]
        val nextEntry = NoticeSnoozeEntry(
            until = now + daysAdd * MS_PER_DAY,
            step = nextStep,
        )
        val merged = _noticeSnooze.value.toMutableMap()
        merged[id] = nextEntry
        val result = resealVault(
            auditLabel = "notice-snooze",
            successMsg = "notice snoozed ${daysAdd}d",
            pushSync = false,
        ) { p ->
            val obj = JSONObject()
            for ((k, v) in merged) {
                obj.put(k, JSONObject().apply {
                    put("until", v.until)
                    put("step", v.step)
                })
            }
            p.put("mobile_notice_snooze", obj)
        }
        if (result.isSuccess) {
            _noticeSnooze.value = merged
        }
    }
}


fun MessengerStore.nextSnoozeLabel(id: String): String {
    val prior = _noticeSnooze.value[id]
    val nextStep = ((prior?.step ?: -1) + 1).coerceAtMost(SNOOZE_DAYS.lastIndex)
    return "Snooze ${SNOOZE_DAYS[nextStep]}d"
}
