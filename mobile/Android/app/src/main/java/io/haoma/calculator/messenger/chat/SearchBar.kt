package io.haoma.calculator.messenger.chat

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.wrapContentHeight
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicText
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material.icons.filled.KeyboardArrowUp
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.ChatSearchState


@Composable
internal fun SearchBar(
    state: ChatSearchState,
    onQueryChange: (String) -> Unit,
    onSubmit: (String) -> Unit,
    onStepOlder: () -> Unit,
    onStepNewer: () -> Unit,
    onClose: () -> Unit,
) {
    val focusRequester = remember { FocusRequester() }
    val keyboard = LocalSoftwareKeyboardController.current
    val counter = formatCounter(state)

    LaunchedEffect(state.chatId) {
        
        
        focusRequester.requestFocus()
        keyboard?.show()
    }

    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(ChatPalette.InboundBubble)
            .padding(horizontal = 8.dp, vertical = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(2.dp),
    ) {
        Surface(
            modifier = Modifier
                .weight(1f)
                .heightIn(min = SearchInputMinHeight),
            shape = RoundedCornerShape(SearchInputCornerRadius),
            color = ChatPalette.Surface,
            border = BorderStroke(width = 1.dp, color = ChatPalette.TextFaint),
        ) {
            BasicTextField(
                value = state.query,
                onValueChange = onQueryChange,
                modifier = Modifier
                    .fillMaxWidth()
                    .wrapContentHeight(Alignment.CenterVertically)
                    .padding(horizontal = 12.dp, vertical = 6.dp)
                    .focusRequester(focusRequester),
                textStyle = TextStyle(color = ChatPalette.Text, fontSize = 14.sp),
                cursorBrush = SolidColor(ChatPalette.Accent),
                singleLine = true,
                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search),
                keyboardActions = KeyboardActions(
                    onSearch = { onSubmit(state.query) },
                ),
                decorationBox = { inner ->
                    Box(modifier = Modifier.fillMaxWidth()) {
                        if (state.query.isEmpty()) {
                            BasicText(
                                text = "Search messages",
                                style = TextStyle(
                                    color = ChatPalette.TextDim,
                                    fontSize = 14.sp,
                                ),
                            )
                        }
                        inner()
                    }
                },
            )
        }
        Text(
            text = counter,
            color = if (state.matches.isEmpty()) ChatPalette.TextDim else ChatPalette.Accent,
            fontSize = 12.sp,
            modifier = Modifier.padding(horizontal = 6.dp),
        )
        IconButton(
            onClick = onStepOlder,
            modifier = Modifier.size(36.dp),
        ) {
            Icon(
                imageVector = Icons.Filled.KeyboardArrowUp,
                contentDescription = "Older match",
                tint = if (state.matches.isEmpty()) ChatPalette.TextFaint else SearchActionColor,
            )
        }
        IconButton(
            onClick = onStepNewer,
            modifier = Modifier.size(36.dp),
        ) {
            Icon(
                imageVector = Icons.Filled.KeyboardArrowDown,
                contentDescription = "Newer match",
                tint = if (state.matches.isEmpty()) ChatPalette.TextFaint else SearchActionColor,
            )
        }
        IconButton(
            onClick = {
                keyboard?.hide()
                onClose()
            },
            modifier = Modifier.size(36.dp),
        ) {
            Icon(
                imageVector = Icons.Filled.Close,
                contentDescription = "Close search",
                tint = ChatPalette.Text,
            )
        }
    }
}

private fun formatCounter(state: ChatSearchState): String {
    if (state.matches.isEmpty()) {
        if (state.query.isEmpty()) return "—/—"
        if (state.loading) return "…"
        return "0/0"
    }
    val suffix = if (state.truncated) "+" else ""
    return "${state.cursorIdx + 1}/${state.matches.size}$suffix"
}

private val SearchInputMinHeight = 36.dp
private val SearchInputCornerRadius = 18.dp


private val SearchActionColor = androidx.compose.ui.graphics.Color(0xFFD79921)
