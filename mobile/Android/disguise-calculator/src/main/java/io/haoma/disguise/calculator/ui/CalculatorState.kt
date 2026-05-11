package io.haoma.disguise.calculator.ui

import io.haoma.disguise.calculator.expr.EvalResult
import io.haoma.disguise.calculator.expr.evaluate


data class CalculatorState(
    val input: String = "",
    val display: String = "0",
    val error: Boolean = false,
)

sealed class CalcAction {
    
    data class Char(val c: String) : CalcAction()
    
    data object Equals : CalcAction()
    
    data object Backspace : CalcAction()
    
    data object Clear : CalcAction()
}

fun reduce(state: CalculatorState, action: CalcAction): CalculatorState {
    
    if (state.error) {
        return when (action) {
            CalcAction.Clear, CalcAction.Backspace -> CalculatorState()
            CalcAction.Equals -> CalculatorState()
            is CalcAction.Char -> CalculatorState(input = action.c, display = action.c)
        }
    }

    return when (action) {
        CalcAction.Clear -> CalculatorState()

        CalcAction.Backspace -> {
            val next = state.input.dropLast(1)
            state.copy(input = next, display = next.ifEmpty { "0" })
        }

        is CalcAction.Char -> {
            val next = state.input + action.c
            state.copy(input = next, display = next)
        }

        CalcAction.Equals -> {
            if (state.input.isEmpty()) return state
            when (val r = evaluate(state.input)) {
                is EvalResult.Ok -> {
                    val text = formatNumber(r.value)
                    state.copy(input = text, display = text, error = false)
                }
                is EvalResult.Err -> state.copy(display = "Error", error = true)
            }
        }
    }
}
