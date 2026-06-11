package io.haoma.calculator.messenger.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.CtaButton
import io.haoma.calculator.messenger.HaomaPalette
import io.haoma.calculator.messenger.HaomaTypography
import io.haoma.calculator.messenger.LocalHaomaTypography
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.Reaction
import io.haoma.calculator.messenger.Section
import io.haoma.calculator.messenger.chat.ChatPalette
import io.haoma.calculator.messenger.chat.ReactionPills
import io.haoma.calculator.messenger.setChatFontScale
import kotlin.math.roundToInt


@Composable
internal fun AppearanceSection(store: MessengerStore, onBack: () -> Unit) {
    val health by store.health.collectAsStateWithLifecycle()
    val scale = health.chatFontScale
    val typography = remember(scale) { HaomaTypography(scale) }
    val atMin = scale <= HaomaTypography.STOPS.first() + EPS
    val atMax = scale >= HaomaTypography.STOPS.last() - EPS
    val atDefault = kotlin.math.abs(scale - DEFAULT_SCALE) < EPS

    Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
        SectionHeader(title = "Appearance", store = store, onBack = onBack)
        Column(modifier = Modifier.weight(1f).verticalScroll(rememberScrollState())) {
            Section(
                label = "Chat font size",
                description = "Applies to every chat. − and + step through six sizes; ⟲ snaps back to the default.",
            ) {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.spacedBy(10.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    CtaButton(
                        label = "−",
                        accent = HaomaPalette.BTN_GIVE,
                        enabled = !atMin,
                    ) { store.setChatFontScale(HaomaTypography.stepDown(scale)) }
                    CtaButton(
                        label = "⟲ Default",
                        accent = HaomaPalette.FG_LINK,
                        enabled = !atDefault,
                    ) { store.setChatFontScale(DEFAULT_SCALE) }
                    CtaButton(
                        label = "+",
                        accent = HaomaPalette.BTN_GIVE,
                        enabled = !atMax,
                    ) { store.setChatFontScale(HaomaTypography.stepUp(scale)) }
                }
                Spacer(modifier = Modifier.height(8.dp))
                Text(
                    text = "${(scale * 100).roundToInt()}%",
                    color = HaomaPalette.FG_DIM,
                    fontSize = 12.sp,
                )
            }
            Section(
                label = "Preview",
            ) {
                CompositionLocalProvider(LocalHaomaTypography provides typography) {
                    PreviewBubble()
                }
            }
            Spacer(modifier = Modifier.height(24.dp))
        }
    }
}


@Composable
private fun PreviewBubble() {
    val type = LocalHaomaTypography.current
    Box(
        modifier = Modifier
            .widthIn(max = 280.dp)
            .clip(RoundedCornerShape(12.dp))
            .background(ChatPalette.InboundBubble)
            .padding(horizontal = 10.dp, vertical = 6.dp),
    ) {
        Column {
            Text(
                text = "Hey, this is roughly how messages look.",
                color = ChatPalette.Text,
                fontSize = type.bubbleBody,
            )
            Row(
                modifier = Modifier.padding(top = 2.dp),
                horizontalArrangement = Arrangement.spacedBy(4.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(
                    text = "12:34",
                    color = ChatPalette.TextDim,
                    fontSize = type.bubbleSmall,
                )
                Text(
                    text = "(edited)",
                    color = ChatPalette.TextDim,
                    fontStyle = FontStyle.Italic,
                    fontSize = type.bubbleSmall,
                )
            }
        }
    }
    
    
    ReactionPills(
        reactions = PREVIEW_REACTIONS,
        modifier = Modifier.offset(y = (-10).dp),
    )
}

private const val DEFAULT_SCALE = 1.0f
private const val EPS = 0.001f


private val PREVIEW_REACTIONS: Map<String, Reaction> = mapOf(
    "peer-a" to Reaction(peerId = "peer-a", emoji = "👍", at = 0L),
    "self" to Reaction(peerId = "", emoji = "❤", at = 0L),
)
