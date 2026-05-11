package io.haoma.disguise.calculator.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.pointer.AwaitPointerEventScope
import androidx.compose.ui.input.pointer.PointerInputScope
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.LayoutCoordinates
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.disguise.DisguiseTip
import io.haoma.disguise.RevealController
import io.haoma.disguise.calculator.RevealConfig
import io.haoma.disguise.calculator.TokenAccumulator
import kotlinx.coroutines.withTimeoutOrNull


@Composable
fun CalculatorScreen(
    reveal: RevealController,
    config: RevealConfig = RevealConfig(),
    modifier: Modifier = Modifier,
    pendingTip: DisguiseTip? = null,
    onTipDismissed: () -> Unit = {},
) {
    var state by remember { mutableStateOf(CalculatorState()) }
    val onAction: (CalcAction) -> Unit = { state = reduce(state, it) }
    var armed by remember { mutableStateOf(false) }

    Box(modifier = modifier.fillMaxSize()) {
        Column(
            modifier = Modifier
                .fillMaxSize()
                .background(GruvboxDark.Bg0),
        ) {
            Display(
                text = state.display,
                error = state.error,
                armed = armed,
                modifier = Modifier
                    .fillMaxWidth()
                    .weight(1f),
            )
            Keypad(
                onAction = onAction,
                reveal = reveal,
                config = config,
                onArmedChanged = { armed = it },
                modifier = Modifier
                    .fillMaxWidth()
                    .weight(2.2f),
            )
        }
        if (pendingTip != null) {
            TipOverlay(tip = pendingTip, onDismiss = onTipDismissed)
        }
    }
}


@Composable
private fun TipOverlay(tip: DisguiseTip, onDismiss: () -> Unit) {
    Box(
        modifier = Modifier
            .fillMaxSize()
            .background(GruvboxDark.Bg0.copy(alpha = 0.85f))
            
            
            .clickable(
                interactionSource = remember { MutableInteractionSource() },
                indication = null,
                onClick = {},
            ),
        contentAlignment = Alignment.Center,
    ) {
        Column(
            modifier = Modifier
                .widthIn(max = 320.dp)
                .padding(24.dp)
                .clip(RoundedCornerShape(12.dp))
                .background(GruvboxDark.Bg1)
                .padding(24.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Text(
                text = tip.title,
                color = GruvboxDark.Accent,
                fontSize = 14.sp,
                fontWeight = FontWeight.SemiBold,
            )
            Spacer(modifier = Modifier.height(12.dp))
            Text(
                text = tip.body,
                color = GruvboxDark.Fg,
                fontSize = 18.sp,
                fontWeight = FontWeight.Normal,
                textAlign = TextAlign.Center,
            )
            Spacer(modifier = Modifier.height(20.dp))
            Button(
                onClick = onDismiss,
                colors = ButtonDefaults.buttonColors(
                    containerColor = GruvboxDark.Bg2,
                    contentColor = GruvboxDark.Accent,
                ),
            ) {
                Text("Got it", fontSize = 14.sp)
            }
        }
    }
}

@Composable
private fun Display(text: String, error: Boolean, armed: Boolean, modifier: Modifier) {
    Box(
        modifier = modifier
            .background(GruvboxDark.Bg0)
            .padding(horizontal = 24.dp, vertical = 16.dp),
    ) {
        if (armed) {
            Box(
                modifier = Modifier
                    .align(Alignment.TopEnd)
                    .size(8.dp)
                    .clip(CircleShape)
                    .background(GruvboxDark.ArmedDot),
            )
        }
        Text(
            text = text,
            color = if (error) GruvboxDark.ErrorFg else GruvboxDark.Fg,
            fontSize = 56.sp,
            fontWeight = FontWeight.Light,
            textAlign = TextAlign.End,
            modifier = Modifier.align(Alignment.BottomEnd),
        )
    }
}

private enum class Slot { Digit, Op, Util, Equals }

private data class Key(
    val label: String,
    val slot: Slot,
    val action: CalcAction,
    val widthWeight: Float = 1f,
)

private val keypadRows: List<List<Key>> = listOf(
    listOf(
        Key("C",  Slot.Util, CalcAction.Clear),
        Key("⌫",  Slot.Util, CalcAction.Backspace),
        Key("(",  Slot.Op,   CalcAction.Char("(")),
        Key(")",  Slot.Op,   CalcAction.Char(")")),
    ),
    listOf(
        Key("√",  Slot.Op,  CalcAction.Char("√")),
        Key("^",  Slot.Op,  CalcAction.Char("^")),
        Key("%",  Slot.Op,  CalcAction.Char("%")),
        Key("÷",  Slot.Op,  CalcAction.Char("÷")),
    ),
    listOf(
        Key("7",  Slot.Digit, CalcAction.Char("7")),
        Key("8",  Slot.Digit, CalcAction.Char("8")),
        Key("9",  Slot.Digit, CalcAction.Char("9")),
        Key("×",  Slot.Op,    CalcAction.Char("×")),
    ),
    listOf(
        Key("4",  Slot.Digit, CalcAction.Char("4")),
        Key("5",  Slot.Digit, CalcAction.Char("5")),
        Key("6",  Slot.Digit, CalcAction.Char("6")),
        Key("−",  Slot.Op,    CalcAction.Char("-")),
    ),
    listOf(
        Key("1",  Slot.Digit, CalcAction.Char("1")),
        Key("2",  Slot.Digit, CalcAction.Char("2")),
        Key("3",  Slot.Digit, CalcAction.Char("3")),
        Key("+",  Slot.Op,    CalcAction.Char("+")),
    ),
    listOf(
        Key("0",  Slot.Digit, CalcAction.Char("0"), widthWeight = 2f),
        Key(".",  Slot.Digit, CalcAction.Char(".")),
        Key("=",  Slot.Equals, CalcAction.Equals),
    ),
)

@Composable
private fun Keypad(
    onAction: (CalcAction) -> Unit,
    reveal: RevealController,
    config: RevealConfig,
    onArmedChanged: (Boolean) -> Unit,
    modifier: Modifier,
) {
    val keyCoords = remember { mutableStateMapOf<String, LayoutCoordinates>() }
    var containerCoords by remember { mutableStateOf<LayoutCoordinates?>(null) }
    val keysByLabel = remember { keypadRows.flatten().associateBy { it.label } }

    val keyAt: (Offset) -> String? = { pos ->
        val cc = containerCoords
        if (cc == null) {
            null
        } else {
            keyCoords.entries.firstOrNull { (_, kc) ->
                kc.isAttached && cc.localBoundingBoxOf(kc).contains(pos)
            }?.key
        }
    }

    
    val keyAtSlide: (Offset) -> String? = { pos ->
        val cc = containerCoords
        if (cc == null) {
            null
        } else {
            keyCoords.entries.firstOrNull { (_, kc) ->
                if (!kc.isAttached) return@firstOrNull false
                val rect = cc.localBoundingBoxOf(kc)
                val halfMin = minOf(rect.width, rect.height) / 2f
                val radius = halfMin * config.slideHitRadiusFraction
                (pos - rect.center).getDistance() <= radius
            }?.key
        }
    }

    Column(
        modifier = modifier
            .padding(8.dp)
            .onGloballyPositioned { containerCoords = it }
            .pointerInput(config) {
                runRevealGesture(
                    config = config,
                    keyAt = keyAt,
                    keyAtSlide = keyAtSlide,
                    keysByLabel = keysByLabel,
                    reveal = reveal,
                    onAction = onAction,
                    onArmedChanged = onArmedChanged,
                )
            },
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        keypadRows.forEach { row ->
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .weight(1f),
                horizontalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                row.forEach { key ->
                    KeyButton(
                        key = key,
                        modifier = Modifier
                            .fillMaxSize()
                            .weight(key.widthWeight)
                            .onGloballyPositioned { keyCoords[key.label] = it },
                    )
                }
            }
        }
    }
}

@Composable
private fun KeyButton(
    key: Key,
    modifier: Modifier = Modifier,
) {
    val (bg, fg) = when (key.slot) {
        Slot.Digit  -> GruvboxDark.Bg to GruvboxDark.Fg
        Slot.Op     -> GruvboxDark.Bg1 to GruvboxDark.Fg2
        Slot.Util   -> GruvboxDark.Bg1 to GruvboxDark.Fg2
        Slot.Equals -> GruvboxDark.Bg2 to GruvboxDark.Accent
    }
    Box(
        modifier = modifier
            .clip(RoundedCornerShape(10.dp))
            .background(bg),
        contentAlignment = Alignment.Center,
    ) {
        Text(
            text = key.label,
            color = fg,
            fontSize = 28.sp,
            fontWeight = FontWeight.Medium,
        )
    }
}


private suspend fun PointerInputScope.runRevealGesture(
    config: RevealConfig,
    keyAt: (Offset) -> String?,
    keyAtSlide: (Offset) -> String?,
    keysByLabel: Map<String, Key>,
    reveal: RevealController,
    onAction: (CalcAction) -> Unit,
    onArmedChanged: (Boolean) -> Unit,
) {
    awaitPointerEventScope {
        while (true) {
            val down = awaitFirstDown(requireUnconsumed = false)
            val downKey = keyAt(down.position)

            if (downKey != config.triggerKey) {
                handleTap(downKey, keyAt, keysByLabel, onAction)
                continue
            }

            
            val holdOutcome = withTimeoutOrNull(config.holdMillis) {
                while (true) {
                    val ev = awaitPointerEvent()
                    val change = ev.changes.first()
                    if (!change.pressed) return@withTimeoutOrNull HoldOutcome.LiftedBeforeArm
                    if (keyAt(change.position) != config.triggerKey) {
                        return@withTimeoutOrNull HoldOutcome.MovedBeforeArm
                    }
                }
                @Suppress("UNREACHABLE_CODE") HoldOutcome.LiftedBeforeArm
            }

            when (holdOutcome) {
                null -> handleArmed(config, keyAtSlide, reveal, onArmedChanged)
                HoldOutcome.LiftedBeforeArm -> {
                    keysByLabel[config.triggerKey]?.action?.let(onAction)
                }
                HoldOutcome.MovedBeforeArm -> drainUntilLift()
            }
        }
    }
}

private enum class HoldOutcome { LiftedBeforeArm, MovedBeforeArm }


private suspend fun AwaitPointerEventScope.handleTap(
    downKey: String?,
    keyAt: (Offset) -> String?,
    keysByLabel: Map<String, Key>,
    onAction: (CalcAction) -> Unit,
) {
    var endKey = downKey
    while (true) {
        val ev = awaitPointerEvent()
        val change = ev.changes.first()
        endKey = keyAt(change.position)
        if (!change.pressed) break
    }
    if (downKey != null && endKey == downKey) {
        keysByLabel[downKey]?.action?.let(onAction)
    }
}


private suspend fun AwaitPointerEventScope.handleArmed(
    config: RevealConfig,
    keyAt: (Offset) -> String?,
    reveal: RevealController,
    onArmedChanged: (Boolean) -> Unit,
) {
    onArmedChanged(true)
    reveal.arm()
    val acc = TokenAccumulator(config.triggerKey)

    val outcome = withTimeoutOrNull(config.armWindowMillis) {
        while (true) {
            val ev = awaitPointerEvent()
            val change = ev.changes.first()
            if (!change.pressed) return@withTimeoutOrNull ArmedOutcome.Lifted
            keyAt(change.position)?.let(acc::visit)
        }
        @Suppress("UNREACHABLE_CODE") ArmedOutcome.Lifted
    }

    onArmedChanged(false)

    when (outcome) {
        null -> {
            
            reveal.cancel()
            drainUntilLift()
        }
        ArmedOutcome.Lifted -> {
            if (acc.isEmpty) reveal.cancel() else reveal.submit(acc.token)
        }
    }
}

private enum class ArmedOutcome { Lifted }


private suspend fun AwaitPointerEventScope.drainUntilLift() {
    while (true) {
        val ev = awaitPointerEvent()
        if (!ev.changes.first().pressed) return
    }
}
