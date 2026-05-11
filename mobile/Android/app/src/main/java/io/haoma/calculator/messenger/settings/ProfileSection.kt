package io.haoma.calculator.messenger.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.*
import io.haoma.calculator.messenger.MessengerStore


@Composable
internal fun ProfileSection(store: MessengerStore, onBack: () -> Unit) {
    val health by store.health.collectAsStateWithLifecycle()
    val initialNick = health.selfNick
    var draft by remember(initialNick) { mutableStateOf(initialNick) }
    val trimmed = draft.trim()
    val dirty by remember(draft, initialNick) {
        derivedStateOf { trimmed != initialNick && trimmed.isNotEmpty() }
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(BG_BASE)
            .verticalScroll(rememberScrollState()),
    ) {
        SectionHeader(title = "Profile", onBack = onBack)

        Section(label = "Self nick") {
            OutlinedTextField(
                value = draft,
                onValueChange = { draft = it },
                singleLine = true,
                placeholder = {
                    Text(
                        text = "(your displayed name)",
                        color = FG_DIM,
                        fontSize = 13.sp,
                    )
                },
                colors = textFieldColors(),
                modifier = Modifier.fillMaxWidth(),
            )
            Spacer(modifier = Modifier.height(4.dp))
            if (health.selfNickIsDefault && initialNick.isNotEmpty()) {
                Text(
                    text = "Currently using a default nick — set yours so paired peers see who you are.",
                    color = FG_DIM,
                    fontSize = 12.sp,
                )
                Spacer(modifier = Modifier.height(4.dp))
            }
            Row {
                Button(
                    enabled = dirty,
                    onClick = {
                        store.setSelfNick(trimmed)
                        onBack()
                    },
                    colors = ButtonDefaults.buttonColors(
                        containerColor = BTN_PRIMARY,
                        contentColor = BG_BASE,
                        disabledContainerColor = BTN_DIM,
                        disabledContentColor = FG_DIM,
                    ),
                ) {
                    Text("Save")
                }
                Spacer(modifier = Modifier.width(12.dp))
                TextButton(
                    enabled = draft != initialNick,
                    onClick = { draft = initialNick },
                ) {
                    Text("Reset", color = if (draft != initialNick) FG_LINK else FG_DIM)
                }
            }
        }

        Spacer(modifier = Modifier.height(24.dp))
    }
}

@Composable
private fun Section(label: String, content: @Composable () -> Unit) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 14.dp),
    ) {
        Text(
            text = label.uppercase(),
            color = FG_DIM,
            fontSize = 11.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Spacer(modifier = Modifier.height(8.dp))
        content()
    }
    HorizontalDivider(color = DIVIDER, thickness = 0.5.dp)
}

@Composable
private fun textFieldColors() = OutlinedTextFieldDefaults.colors(
    focusedTextColor = FG_PRIMARY,
    unfocusedTextColor = FG_PRIMARY,
    cursorColor = FG_LINK,
    focusedBorderColor = FG_LINK,
    unfocusedBorderColor = DIVIDER,
)

private val BG_BASE = Color(0xFF1D2021)
private val DIVIDER = Color(0xFF3C3836)
private val FG_PRIMARY = Color(0xFFEBDBB2)
private val FG_DIM = Color(0xFF7C6F64)
private val FG_LINK = Color(0xFF83A598)
private val BTN_PRIMARY = Color(0xFF5FCC1A)
private val BTN_DIM = Color(0xFF504945)
