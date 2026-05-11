package io.haoma.calculator.messenger.chat

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicText
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.PlatformTextStyle
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.style.LineHeightStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import io.haoma.calculator.messenger.Reaction


@OptIn(ExperimentalLayoutApi::class)
@Composable
internal fun ReactionPills(
    reactions: Map<String, Reaction>,
    onTap: (emoji: String) -> Unit = {},
    modifier: Modifier = Modifier,
) {
    if (reactions.isEmpty()) return
    val grouped = groupByEmoji(reactions.values)
    FlowRow(
        modifier = modifier.padding(top = 4.dp),
        horizontalArrangement = Arrangement.spacedBy(4.dp),
        verticalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        for ((emoji, count, mine) in grouped) {
            ReactionPill(emoji = emoji, count = count, mine = mine, onTap = { onTap(emoji) })
        }
    }
}

@Composable
private fun ReactionPill(emoji: String, count: Int, mine: Boolean, onTap: () -> Unit) {
    val borderColor = if (mine) ChatPalette.Accent else ChatPalette.TextFaint
    
    
    val multi = count > 1
    val shape = if (multi) PillShape else CircleShape
    val sizing = if (multi) {
        Modifier.height(PillSize).widthIn(min = PillSize)
    } else {
        Modifier.size(PillSize)
    }
    val text = if (multi) "$emoji $count" else emoji
    Box(
        modifier = Modifier
            .then(sizing)
            .clip(shape)
            .background(ChatPalette.InboundBubble)
            .border(width = 0.5.dp, color = borderColor, shape = shape)
            .clickable(onClick = onTap)
            .padding(horizontal = if (multi) 6.dp else 0.dp),
        contentAlignment = Alignment.Center,
    ) {
        BasicText(
            text = text,
            style = PillTextStyle,
        )
    }
}


private val PillTextStyle = TextStyle(
    color = ChatPalette.Text,
    fontSize = 11.sp,
    lineHeight = 11.sp,
    platformStyle = PlatformTextStyle(includeFontPadding = false),
    lineHeightStyle = LineHeightStyle(
        alignment = LineHeightStyle.Alignment.Center,
        trim = LineHeightStyle.Trim.Both,
    ),
)

private data class PillRow(val emoji: String, val count: Int, val mine: Boolean)

private fun groupByEmoji(values: Collection<Reaction>): List<PillRow> {
    val byEmoji = LinkedHashMap<String, MutableList<Reaction>>()
    for (r in values) {
        byEmoji.getOrPut(r.emoji) { mutableListOf() }.add(r)
    }
    return byEmoji.map { (emoji, list) ->
        PillRow(emoji = emoji, count = list.size, mine = list.any { it.isMine })
    }
}

private val PillShape = RoundedCornerShape(50)
private val PillSize = 20.dp
