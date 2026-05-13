package io.haoma.calculator.messenger.status

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.StatusLevel
import io.haoma.calculator.messenger.StatusLine
import io.haoma.calculator.messenger.StatusSource
import io.haoma.calculator.messenger.runCommand


@Composable
fun CmdSection(store: MessengerStore) {
    val log by store.statusLog.collectAsStateWithLifecycle()
    val cliLog = log.filter { it.source == StatusSource.Cli }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(BG_BASE)
            .padding(horizontal = 12.dp, vertical = 8.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Box(modifier = Modifier.weight(1f).fillMaxWidth()) {
            StatusLogList(log = cliLog)
        }
        CommandInput(store = store)
    }
}

@Composable
private fun CommandInput(store: MessengerStore) {
    var input by remember { mutableStateOf("") }
    Surface(
        modifier = Modifier
            .fillMaxWidth()
            .heightIn(min = CommandInputMinHeight),
        shape = RoundedCornerShape(percent = 50),
        color = BG_BAR,
        border = BorderStroke(1.dp, BORDER_CMD),
    ) {
        BasicTextField(
            value = input,
            onValueChange = { input = it },
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 14.dp, vertical = 8.dp),
            singleLine = true,
            textStyle = TextStyle(
                color = FG_LOG,
                fontFamily = FontFamily.Monospace,
                fontSize = 13.sp,
            ),
            cursorBrush = SolidColor(BORDER_CMD),
            keyboardOptions = KeyboardOptions(imeAction = ImeAction.Send),
            keyboardActions = KeyboardActions(
                onSend = {
                    store.runCommand(input)
                    input = ""
                },
            ),
            decorationBox = { inner ->
                if (input.isEmpty()) {
                    Text(
                        text = "/help",
                        color = FG_DIM,
                        fontFamily = FontFamily.Monospace,
                        fontSize = 13.sp,
                    )
                }
                inner()
            },
        )
    }
}

@Composable
private fun StatusLogList(log: List<StatusLine>) {
    val listState = rememberLazyListState()
    LaunchedEffect(log.size) {
        if (log.isNotEmpty()) listState.scrollToItem(log.size - 1)
    }
    if (log.isEmpty()) {
        Text(
            text = "(no commands yet — type /help)",
            color = FG_DIM,
            fontFamily = FontFamily.Monospace,
            fontSize = 13.sp,
        )
        return
    }
    LazyColumn(
        state = listState,
        modifier = Modifier.fillMaxSize(),
        contentPadding = PaddingValues(vertical = 4.dp),
    ) {
        items(log, key = { it.at.toString() + it.text }) { line ->
            val color = when (line.level) {
                StatusLevel.WARN -> C_WARN
                else -> FG_LOG
            }
            Row(modifier = Modifier.fillMaxWidth().padding(vertical = 1.dp)) {
                Text(
                    text = line.stamp(),
                    color = FG_DIM,
                    fontFamily = FontFamily.Monospace,
                    fontSize = 12.sp,
                )
                Text(
                    text = "  " + line.text,
                    color = color,
                    fontFamily = FontFamily.Monospace,
                    fontSize = 12.sp,
                )
            }
        }
    }
}

private val BG_BASE = Color(0xFF1D2021)
private val BG_BAR = Color(0xFF282828)
private val FG_LOG = Color(0xFFD5C4A1)
private val FG_DIM = Color(0xFF7C6F64)
private val C_WARN = Color(0xFFFABD2F)
private val BORDER_CMD = Color(0xFF83A598)
private val CommandInputMinHeight = 40.dp
