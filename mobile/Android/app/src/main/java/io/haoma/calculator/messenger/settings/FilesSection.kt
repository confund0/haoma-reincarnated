package io.haoma.calculator.messenger.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.messenger.HaomaPalette
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.Section


@Composable
internal fun FilesSection(store: MessengerStore, onBack: () -> Unit) {
    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(HaomaPalette.BG_BASE)
            .verticalScroll(rememberScrollState()),
    ) {
        SectionHeader(title = "Files", store = store, onBack = onBack)

        Section(label = "On Android") {
            Text(
                text = "The system file picker handles where attachments come from and where saved files land. It remembers your recent locations automatically.",
                color = HaomaPalette.FG_PRIMARY,
                fontSize = 14.sp,
                lineHeight = 20.sp,
            )
        }

        Section(label = "Desktop scope") {
            Text(
                text = "Default save + attach folders are configured on the desktop app. Those vault fields aren't read on mobile.",
                color = HaomaPalette.FG_SECONDARY,
                fontSize = 13.sp,
                lineHeight = 18.sp,
            )
        }

        Spacer(modifier = Modifier.height(24.dp))
    }
}
