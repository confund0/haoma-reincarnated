package io.haoma.disguise.calculator.expr


internal class Parser(tokens: List<Token>) {
    private val tokens = tokens
    private var i = 0

    fun parse(): Expr {
        val e = expr()
        expect(Token.Eof)
        return e
    }

    private fun expr(): Expr {
        var node = term()
        while (true) {
            node = when (peek()) {
                Token.Plus -> { advance(); Expr.Add(node, term()) }
                Token.Minus -> { advance(); Expr.Sub(node, term()) }
                else -> return node
            }
        }
    }

    private fun term(): Expr {
        var node = factor()
        while (true) {
            node = when (peek()) {
                Token.Times -> { advance(); Expr.Mul(node, factor()) }
                Token.Divide -> { advance(); Expr.Div(node, factor()) }
                else -> return node
            }
        }
    }

    private fun factor(): Expr {
        val base = unary()
        return if (peek() == Token.Power) {
            advance()
            Expr.Pow(base, factor())  
        } else base
    }

    private fun unary(): Expr =
        if (peek() == Token.Minus) {
            advance()
            Expr.Neg(unary())
        } else atom()

    private fun atom(): Expr {
        var node: Expr = when (val t = peek()) {
            is Token.Number -> { advance(); Expr.Num(t.v) }
            Token.LParen -> {
                advance()
                val inner = expr()
                expect(Token.RParen)
                inner
            }
            Token.Sqrt -> {
                advance()
                Expr.Sqrt(atom())
            }
            else -> throw ParseError("Unexpected token: $t")
        }
        while (peek() == Token.Percent) {
            advance()
            node = Expr.Percent(node)
        }
        return node
    }

    private fun peek(): Token = tokens[i]
    private fun advance(): Token = tokens[i++]
    private fun expect(t: Token) {
        val cur = peek()
        if (cur != t) throw ParseError("Expected $t, got $cur")
        advance()
    }
}
