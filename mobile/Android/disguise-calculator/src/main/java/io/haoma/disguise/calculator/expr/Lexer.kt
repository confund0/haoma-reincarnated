package io.haoma.disguise.calculator.expr

internal sealed class Token {
    data class Number(val v: Double) : Token()
    data object Plus : Token()
    data object Minus : Token()
    data object Times : Token()
    data object Divide : Token()
    data object Power : Token()
    data object Sqrt : Token()
    data object Percent : Token()
    data object LParen : Token()
    data object RParen : Token()
    data object Eof : Token()
}


internal class Lexer(private val src: String) {
    private var pos = 0

    fun tokens(): List<Token> {
        val out = mutableListOf<Token>()
        while (pos < src.length) {
            val c = src[pos]
            when {
                c.isWhitespace() -> pos++
                c.isDigit() || c == '.' -> out += readNumber()
                c == '+' -> { pos++; out += Token.Plus }
                c == '-' || c == '−' -> { pos++; out += Token.Minus }
                c == '*' || c == '×' -> { pos++; out += Token.Times }
                c == '/' || c == '÷' -> { pos++; out += Token.Divide }
                c == '^' -> { pos++; out += Token.Power }
                c == '√' -> { pos++; out += Token.Sqrt }
                c == '%' -> { pos++; out += Token.Percent }
                c == '(' -> { pos++; out += Token.LParen }
                c == ')' -> { pos++; out += Token.RParen }
                else -> throw ParseError("Unexpected character: $c")
            }
        }
        out += Token.Eof
        return out
    }

    private fun readNumber(): Token.Number {
        val start = pos
        while (pos < src.length && src[pos].isDigit()) pos++
        if (pos < src.length && src[pos] == '.') {
            pos++
            while (pos < src.length && src[pos].isDigit()) pos++
        }
        val text = src.substring(start, pos)
        
        if (text == ".") throw ParseError("Bad number: '.'")
        return Token.Number(text.toDouble())
    }
}
