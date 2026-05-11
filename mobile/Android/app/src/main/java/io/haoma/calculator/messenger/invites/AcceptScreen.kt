package io.haoma.calculator.messenger.invites

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.*
import io.haoma.calculator.messenger.AcceptResult
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.PairType
import kotlinx.coroutines.launch


@Composable
fun AcceptScreen(
    store: MessengerStore,
    type: PairType,
    onBack: () -> Unit,
) {
    var words by remember { mutableStateOf("") }
    var alias by remember { mutableStateOf("") }
    var error by remember { mutableStateOf<String?>(null) }
    var submitting by remember { mutableStateOf(false) }

    val coroutineScope = rememberCoroutineScope()
    val wordsFocus = remember { FocusRequester() }

    LaunchedEffect(Unit) { wordsFocus.requestFocus() }

    fun submit() {
        if (submitting) return
        val tokens = words.trim().split(Regex("\\s+")).filter { it.isNotEmpty() }
        if (tokens.size < 3) {
            error = "Need the full 7 EFF-short words from your peer's invite."
            return
        }
        if (type != PairType.Tor) {
            error = "${type.label} accept lands with its slice — Tor only for now."
            return
        }
        error = null
        submitting = true
        coroutineScope.launch {
            val result = store.acceptOnion(words = tokens, alias = alias.trim())
            submitting = false
            when (result) {
                is AcceptResult.Ok -> onBack()
                is AcceptResult.Error -> error = result.message
            }
        }
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(BG_BASE),
    ) {
        Header(title = "Accept ${type.label} invite", onBack = onBack)

        Column(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 16.dp, vertical = 14.dp),
        ) {
            Text(
                text = "Type or paste the 7 EFF-short words your peer shared.",
                color = FG_DIM,
                fontSize = 13.sp,
            )

            Spacer(modifier = Modifier.height(12.dp))

            OutlinedTextField(
                value = words,
                onValueChange = { words = it; if (error != null) error = null },
                singleLine = true,
                placeholder = {
                    Text(
                        text = "acid acorn acre acts afar affix aged",
                        color = FG_DIM,
                        fontFamily = FontFamily.Monospace,
                        fontSize = 13.sp,
                    )
                },
                colors = textFieldColors(),
                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done),
                keyboardActions = KeyboardActions(onDone = { submit() }),
                modifier = Modifier
                    .fillMaxWidth()
                    .focusRequester(wordsFocus),
            )

            Spacer(modifier = Modifier.height(14.dp))

            OutlinedTextField(
                value = alias,
                onValueChange = { alias = it },
                singleLine = true,
                placeholder = {
                    Text(
                        text = "Local alias for this peer (optional)",
                        color = FG_DIM,
                        fontSize = 13.sp,
                    )
                },
                colors = textFieldColors(),
                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done),
                keyboardActions = KeyboardActions(onDone = { submit() }),
                modifier = Modifier.fillMaxWidth(),
            )

            Spacer(modifier = Modifier.height(16.dp))

            Row(verticalAlignment = Alignment.CenterVertically) {
                Button(
                    enabled = !submitting && words.isNotBlank(),
                    onClick = { submit() },
                    colors = ButtonDefaults.buttonColors(
                        containerColor = BTN_PRIMARY,
                        contentColor = BG_BASE,
                        disabledContainerColor = BTN_DIM,
                        disabledContentColor = FG_DIM,
                    ),
                ) {
                    Text(if (submitting) "Pairing…" else "Submit")
                }
                if (submitting) {
                    Spacer(modifier = Modifier.width(12.dp))
                    CircularProgressIndicator(
                        color = BTN_PRIMARY,
                        strokeWidth = 2.dp,
                        modifier = Modifier
                            .height(20.dp)
                            .width(20.dp),
                    )
                    Spacer(modifier = Modifier.width(12.dp))
                    Text(
                        text = "dialing peer's onion (5–30s)…",
                        color = FG_DIM,
                        fontSize = 12.sp,
                    )
                }
            }

            error?.let { message ->
                Spacer(modifier = Modifier.height(12.dp))
                Text(
                    text = message,
                    color = C_DANGER,
                    fontSize = 13.sp,
                )
            }

            Spacer(modifier = Modifier.height(24.dp))

            Text(
                text = "After pairing, you can verify the peer's identity " +
                    "fingerprint from the Contacts tab.",
                color = FG_DIM,
                fontSize = 12.sp,
            )
        }
    }
}

@Composable
private fun Header(title: String, onBack: () -> Unit) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(BG_BAR)
            .padding(horizontal = 8.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = "‹ Back",
            color = FG_LINK,
            fontSize = 16.sp,
            modifier = Modifier
                .clickable(onClick = onBack)
                .padding(horizontal = 6.dp, vertical = 6.dp),
        )
        Spacer(modifier = Modifier.width(8.dp))
        Text(
            text = title,
            color = FG_PRIMARY,
            fontWeight = FontWeight.SemiBold,
            fontSize = 17.sp,
            modifier = Modifier.weight(1f),
        )
    }
}

@Composable
private fun textFieldColors() = OutlinedTextFieldDefaults.colors(
    focusedTextColor = FG_PRIMARY,
    unfocusedTextColor = FG_PRIMARY,
    disabledTextColor = FG_DIM,
    cursorColor = FG_LINK,
    focusedBorderColor = FG_LINK,
    unfocusedBorderColor = DIVIDER,
    disabledBorderColor = DIVIDER,
)


private val BG_BASE = Color(0xFF1D2021)
private val BG_BAR = Color(0xFF282828)
private val DIVIDER = Color(0xFF3C3836)
private val FG_PRIMARY = Color(0xFFEBDBB2)
private val FG_DIM = Color(0xFF7C6F64)
private val FG_LINK = Color(0xFF83A598)
private val BTN_PRIMARY = Color(0xFF5FCC1A)
private val BTN_DIM = Color(0xFF504945)
private val C_DANGER = Color(0xFFCC241D)

