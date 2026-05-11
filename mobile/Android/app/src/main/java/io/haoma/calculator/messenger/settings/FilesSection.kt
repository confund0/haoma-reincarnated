package io.haoma.calculator.messenger.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp


@Composable
internal fun FilesSection(onBack: () -> Unit) {
    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(BG_BASE_FS)
            .verticalScroll(rememberScrollState()),
    ) {
        SectionHeader(title = "Files", onBack = onBack)

        InfoSection(label = "On Android") {
            Text(
                text = "The system file picker handles where attachments come " +
                    "from and where saved files land. It remembers your recent " +
                    "locations automatically.",
                color = FG_PRIMARY_FS,
                fontSize = 14.sp,
            )
        }

        InfoSection(label = "Desktop scope") {
            Text(
                text = "Default save + attach folders are configured on the " +
                    "desktop app. Those vault fields aren't read on mobile.",
                color = FG_DIM_FS,
                fontSize = 13.sp,
            )
        }

        Spacer(modifier = Modifier.height(24.dp))
    }
}

@Composable
private fun InfoSection(label: String, content: @Composable () -> Unit) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 14.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Text(
            text = label.uppercase(),
            color = FG_DIM_FS,
            fontSize = 11.sp,
            fontWeight = FontWeight.SemiBold,
        )
        content()
    }
    HorizontalDivider(color = DIVIDER_FS, thickness = 0.5.dp)
}

private val BG_BASE_FS = Color(0xFF1D2021)
private val DIVIDER_FS = Color(0xFF3C3836)
private val FG_PRIMARY_FS = Color(0xFFEBDBB2)
private val FG_DIM_FS = Color(0xFF7C6F64)
