package io.haoma.calculator.messenger.chat


enum class TextStyleMark { BOLD, ITALIC, UNDERLINE, STRIKE }


data class StyleSpan(val start: Int, val end: Int, val mark: TextStyleMark)


data class StyledText(val text: String, val spans: List<StyleSpan>)


private val BOUNDARY_PUNCT = setOf(
    '.', ',', '!', '?', ';', ':', '(', ')', '[', ']', '{', '}',
    '<', '>', '"', '\'', '*', '_', '~',
)

private fun isBoundary(c: Char?): Boolean =
    c == null || c.isWhitespace() || c in BOUNDARY_PUNCT


private fun markLiteral(mark: TextStyleMark): String = when (mark) {
    TextStyleMark.BOLD -> "*"
    TextStyleMark.ITALIC -> "_"
    TextStyleMark.STRIKE -> "~"
    TextStyleMark.UNDERLINE -> "__"
}


private fun markAt(s: String, i: Int): TextStyleMark? = when (s[i]) {
    '*' -> TextStyleMark.BOLD
    '~' -> TextStyleMark.STRIKE
    '_' -> if (i + 1 < s.length && s[i + 1] == '_') TextStyleMark.UNDERLINE else TextStyleMark.ITALIC
    else -> null
}

private fun markLen(mark: TextStyleMark): Int = if (mark == TextStyleMark.UNDERLINE) 2 else 1

private class OpenMark(val mark: TextStyleMark, val outStart: Int)


fun parseMessageStyling(raw: String): StyledText {
    val out = StringBuilder(raw.length)
    val spans = ArrayList<StyleSpan>()
    val stack = ArrayList<OpenMark>()

    var i = 0
    while (i < raw.length) {
        val mark = markAt(raw, i)
        if (mark == null) {
            out.append(raw[i])
            i++
            continue
        }
        val len = markLen(mark)
        val prev = raw.getOrNull(i - 1)
        val next = raw.getOrNull(i + len)
        val canOpen = isBoundary(prev) && next != null && !next.isWhitespace()
        val canClose = prev != null && !prev.isWhitespace() && isBoundary(next)
        val topMatches = stack.isNotEmpty() && stack.last().mark == mark

        when {
            canClose && topMatches -> {
                val open = stack.removeAt(stack.size - 1)
                
                if (out.length > open.outStart) {
                    spans.add(StyleSpan(open.outStart, out.length, mark))
                }
            }
            canOpen -> stack.add(OpenMark(mark, out.length))
            else -> out.append(markLiteral(mark))
        }
        i += len
    }

    
    while (stack.isNotEmpty()) {
        val open = stack.removeAt(stack.size - 1)
        val lit = markLiteral(open.mark)
        out.insert(open.outStart, lit)
        val n = lit.length
        for (k in spans.indices) {
            val s = spans[k]
            val start = if (s.start >= open.outStart) s.start + n else s.start
            val end = if (s.end > open.outStart) s.end + n else s.end
            spans[k] = StyleSpan(start, end, s.mark)
        }
        for (k in stack.indices) {
            if (stack[k].outStart >= open.outStart) {
                stack[k] = OpenMark(stack[k].mark, stack[k].outStart + n)
            }
        }
    }

    return StyledText(out.toString(), spans)
}
