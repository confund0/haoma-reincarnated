package io.haoma.calculator.messenger.chat

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicText
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Close
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.IconButtonDefaults
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.TimelineEvent


@Composable
internal fun ChatInput(
    onSend: (String) -> Unit,
    editingTarget: TimelineEvent? = null,
    onSubmitEdit: (TimelineEvent, String) -> Unit = { _, _ -> },
    onCancelEdit: () -> Unit = {},
    onAttach: () -> Unit = {},
) {
    var draft by remember { mutableStateOf("") }
    val hasText = draft.isNotBlank()
    val editing = editingTarget != null

    
    LaunchedEffect(editingTarget?.msgId) {
        draft = editingTarget?.bodyTextOrEmpty() ?: ""
    }

    fun submit() {
        if (!hasText) return
        if (editingTarget != null) {
            onSubmitEdit(editingTarget, draft)
        } else {
            onSend(draft)
        }
        draft = ""
    }

    Column(modifier = Modifier.fillMaxWidth()) {
        if (editing) {
            EditingBanner(
                preview = editingTarget?.bodyTextOrEmpty().orEmpty(),
                onCancel = {
                    draft = ""
                    onCancelEdit()
                },
            )
        }
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 8.dp, vertical = 6.dp),
            verticalAlignment = Alignment.Bottom,
            horizontalArrangement = Arrangement.spacedBy(6.dp),
        ) {
            Surface(
                modifier = Modifier
                    .weight(1f)
                    .heightIn(min = InputMinHeight),
                shape = RoundedCornerShape(percent = 50),
                color = ChatPalette.InboundBubble,
                border = BorderStroke(
                    width = 1.dp,
                    color = if (editing) ChatPalette.Accent else ChatPalette.TextFaint,
                ),
            ) {
                BasicTextField(
                    value = draft,
                    onValueChange = { draft = it },
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(horizontal = 14.dp, vertical = 8.dp),
                    textStyle = TextStyle(color = ChatPalette.Text, fontSize = 14.sp),
                    cursorBrush = SolidColor(ChatPalette.Accent),
                    maxLines = 5,
                    decorationBox = { inner ->
                        
                        
                        Box(modifier = Modifier.fillMaxWidth()) {
                            if (draft.isEmpty()) {
                                BasicText(
                                    text = if (editing) "Edit message…" else "Type a message",
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
            SendStack(
                hasText = hasText,
                editing = editing,
                onSend = ::submit,
                onAttach = onAttach,
            )
        }
    }
}

@Composable
private fun EditingBanner(preview: String, onCancel: () -> Unit) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(ChatPalette.Surface)
            .padding(start = 16.dp, end = 4.dp, top = 4.dp, bottom = 0.dp),
        horizontalArrangement = Arrangement.SpaceBetween,
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = "Editing: ${preview.take(40)}${if (preview.length > 40) "…" else ""}",
            color = ChatPalette.Accent,
            fontSize = 11.sp,
            modifier = Modifier.weight(1f, fill = false),
        )
        IconButton(
            onClick = onCancel,
            modifier = Modifier.size(28.dp),
        ) {
            Icon(
                imageVector = Icons.Filled.Close,
                contentDescription = "Cancel edit",
                tint = ChatPalette.TextDim,
            )
        }
    }
}

@Composable
private fun SendStack(
    hasText: Boolean,
    editing: Boolean,
    onSend: () -> Unit,
    onAttach: () -> Unit,
) {
    Box(
        modifier = Modifier
            .size(StackSize)
            .clip(CircleShape),
        contentAlignment = Alignment.Center,
    ) {
        if (hasText) {
            IconButton(
                onClick = onSend,
                colors = IconButtonDefaults.iconButtonColors(
                    contentColor = ChatPalette.Surface,
                    containerColor = ChatPalette.Accent,
                ),
            ) {
                Icon(
                    imageVector = Icons.AutoMirrored.Filled.Send,
                    contentDescription = if (editing) "Save edit" else "Send",
                )
            }
        } else {
            
            
            IconButton(onClick = onAttach) {
                Icon(
                    imageVector = Icons.Filled.Add,
                    contentDescription = "Attach file",
                    tint = ChatPalette.TextDim,
                )
            }
        }
    }
}

private val StackSize = 40.dp
private val InputMinHeight = 40.dp
