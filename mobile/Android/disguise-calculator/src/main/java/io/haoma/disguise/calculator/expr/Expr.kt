package io.haoma.disguise.calculator.expr


sealed class Expr {
    data class Num(val v: Double) : Expr()
    data class Neg(val e: Expr) : Expr()
    data class Add(val l: Expr, val r: Expr) : Expr()
    data class Sub(val l: Expr, val r: Expr) : Expr()
    data class Mul(val l: Expr, val r: Expr) : Expr()
    data class Div(val l: Expr, val r: Expr) : Expr()
    data class Pow(val l: Expr, val r: Expr) : Expr()
    data class Sqrt(val e: Expr) : Expr()
    data class Percent(val e: Expr) : Expr()
}

class ParseError(message: String) : RuntimeException(message)

class EvalError(message: String) : RuntimeException(message)

sealed class EvalResult {
    data class Ok(val value: Double) : EvalResult()
    data class Err(val message: String) : EvalResult()
}


fun evaluate(input: String): EvalResult = try {
    val tokens = Lexer(input).tokens()
    val expr = Parser(tokens).parse()
    EvalResult.Ok(eval(expr))
} catch (e: ParseError) {
    EvalResult.Err(e.message ?: "Parse error")
} catch (e: EvalError) {
    EvalResult.Err(e.message ?: "Error")
} catch (e: ArithmeticException) {
    EvalResult.Err(e.message ?: "Arithmetic error")
}
