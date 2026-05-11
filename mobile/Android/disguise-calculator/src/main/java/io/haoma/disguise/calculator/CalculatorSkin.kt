package io.haoma.disguise.calculator

import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import io.haoma.disguise.DisguiseSkin
import io.haoma.disguise.DisguiseTip
import io.haoma.disguise.RevealController
import io.haoma.disguise.calculator.ui.CalculatorScreen


class CalculatorSkin(
    private val config: RevealConfig = RevealConfig(),
) : DisguiseSkin {
    override val id: String = "calculator"

    @Composable
    override fun Surface(
        reveal: RevealController,
        pendingTip: DisguiseTip?,
        onTipDismissed: () -> Unit,
    ) {
        CalculatorScreen(
            reveal = reveal,
            config = config,
            modifier = Modifier,
            pendingTip = pendingTip,
            onTipDismissed = onTipDismissed,
        )
    }

    override fun nextTip(): DisguiseTip = DisguiseTip(
        title = TIP_TITLE,
        body = TIP_BODIES.random(),
    )

    companion object {
        
        
        private const val TIP_TITLE = "Math Tip"

        
        private val TIP_BODIES = listOf(
            "Did you know √2 is irrational?",
            "Trick: to multiply by 11, add adjacent digits.",
            "Fact: a perfect square has an odd number of divisors.",
            "Tip: 9 × 5 = (10 × 5) − 5 = 45.",
            "Did you know π was approximated by Archimedes to 3.1418?",
            "Fact: 0! = 1, by convention.",
            "Tip: any number ending in 5 squares to a number ending in 25.",
            "Fact: every even number ≥ 4 is the sum of two primes (Goldbach).",
            "Trick: to divide by 5, multiply by 2 and shift the decimal.",
            "Did you know 1/7 = 0.142857142857… repeats forever?",
            "Fact: the digits of 1089 reverse to 9801 — exactly 9 × 1089.",
            "Tip: any 3-digit number with all same digits is divisible by 37.",
        )
    }
}
