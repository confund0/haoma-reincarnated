package io.haoma.calculator.messenger.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.padding
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.CtaButton
import io.haoma.calculator.messenger.HaomaPalette
import io.haoma.calculator.messenger.HaomaTextField
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.Section
import io.haoma.calculator.messenger.setSelfNick


@Composable
internal fun ProfileSection(store: MessengerStore, onBack: () -> Unit) {
    val health by store.health.collectAsStateWithLifecycle()
    val initialNick = health.selfNick
    var draft by remember(initialNick) { mutableStateOf(initialNick) }
    val trimmed = draft.trim()
    val dirty by remember(draft, initialNick) {
        derivedStateOf { trimmed != initialNick && trimmed.isNotEmpty() }
    }
    val canReset = draft != initialNick

    Column(modifier = Modifier.fillMaxSize().background(HaomaPalette.BG_BASE)) {
        SectionHeader(title = "Profile", store = store, onBack = onBack)
        
        
        Column(modifier = Modifier.weight(1f).verticalScroll(rememberScrollState())) {
            Section(
                label = "Self nick",
                description = "The name paired peers see for you. Saved nicks go live immediately and broadcast over the bus.",
            ) {
                HaomaTextField(
                    value = draft,
                    onValueChange = { draft = it },
                    placeholder = "(your displayed name)",
                    modifier = Modifier.fillMaxWidth(),
                )
                if (health.selfNickIsDefault && initialNick.isNotEmpty()) {
                    Spacer(modifier = Modifier.height(6.dp))
                    Text(
                        text = "Currently using a default nick — set yours so paired peers see who you are.",
                        color = HaomaPalette.C_WARN,
                        fontSize = 12.sp,
                    )
                }
                Spacer(modifier = Modifier.height(14.dp))
                Row(
                    horizontalArrangement = Arrangement.spacedBy(12.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    CtaButton(
                        label = "Save",
                        accent = HaomaPalette.BTN_PRIMARY,
                        enabled = dirty,
                    ) {
                        
                        
                        store.setSelfNick(trimmed)
                    }
                    Text(
                        text = "Reset",
                        color = if (canReset) HaomaPalette.FG_LINK else HaomaPalette.FG_DIM,
                        fontSize = 13.sp,
                        fontWeight = FontWeight.SemiBold,
                        modifier = Modifier
                            .clickable(enabled = canReset) { draft = initialNick }
                            .padding(horizontal = 8.dp, vertical = 10.dp),
                    )
                }
            }
            Spacer(modifier = Modifier.height(24.dp))
        }
    }
}
