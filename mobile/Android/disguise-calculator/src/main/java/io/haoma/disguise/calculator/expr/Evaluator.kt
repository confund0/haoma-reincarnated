package io.haoma.disguise.calculator.expr

import kotlin.math.pow
import kotlin.math.sqrt


internal fun eval(e: Expr): Double = when (e) {
    is Expr.Num -> e.v
    is Expr.Neg -> -eval(e.e)
    is Expr.Add -> {
        val l = eval(e.l)
        l + percentRhs(l, e.r) { eval(e.r) }
    }
    is Expr.Sub -> {
        val l = eval(e.l)
        l - percentRhs(l, e.r) { eval(e.r) }
    }
    is Expr.Mul -> {
        val l = eval(e.l)
        val r = if (e.r is Expr.Percent) eval(e.r.e) / 100.0 else eval(e.r)
        l * r
    }
    is Expr.Div -> {
        val l = eval(e.l)
        val r = if (e.r is Expr.Percent) eval(e.r.e) / 100.0 else eval(e.r)
        if (r == 0.0) throw EvalError("Divide by zero")
        l / r
    }
    is Expr.Pow -> eval(e.l).pow(eval(e.r))
    is Expr.Sqrt -> {
        val v = eval(e.e)
        if (v < 0.0) throw EvalError("√ of negative")
        sqrt(v)
    }
    is Expr.Percent -> eval(e.e) / 100.0  
}


private inline fun percentRhs(left: Double, rhs: Expr, eager: () -> Double): Double =
    if (rhs is Expr.Percent) left * eval(rhs.e) / 100.0 else eager()
