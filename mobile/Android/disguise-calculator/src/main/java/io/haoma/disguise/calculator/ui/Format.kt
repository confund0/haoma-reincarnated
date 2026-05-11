package io.haoma.disguise.calculator.ui

import kotlin.math.abs


internal fun formatNumber(d: Double): String {
    if (d.isNaN() || d.isInfinite()) return "Error"
    if (d == 0.0) return "0"

    val a = abs(d)
    if (a >= 1e12 || a < 1e-9) {
        return "%.6e".format(d)
    }
    if (d == d.toLong().toDouble()) {
        return d.toLong().toString()
    }
    return "%.10f".format(d).trimEnd('0').trimEnd('.')
}
