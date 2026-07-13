package io.haoma.calculator.messenger.chat

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.Text
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp


@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun AttachSourceSheet(
    onDismiss: () -> Unit,
    onGallery: () -> Unit,
    onFiles: () -> Unit,
) {
    val sheetState = rememberModalBottomSheetState()
    ModalBottomSheet(
        onDismissRequest = onDismiss,
        sheetState = sheetState,
        containerColor = ChatPalette.Surface,
    ) {
        Column(modifier = Modifier.padding(bottom = 16.dp)) {
            SourceItem(glyph = "🖼", label = "Gallery", onClick = onGallery)
            SourceItem(glyph = "📁", label = "Files", onClick = onFiles)
        }
    }
}

@Composable
private fun SourceItem(glyph: String, label: String, onClick: () -> Unit) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(ChatPalette.Surface)
            .clickable(onClick = onClick)
            .padding(horizontal = 24.dp, vertical = 14.dp),
        horizontalArrangement = Arrangement.spacedBy(14.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = glyph,
            color = ChatPalette.Text,
            fontSize = 18.sp,
            textAlign = TextAlign.Center,
            modifier = Modifier.width(22.dp),
        )
        Text(
            text = label,
            color = ChatPalette.Text,
            fontSize = 16.sp,
        )
    }
}
