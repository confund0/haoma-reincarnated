package io.haoma.calculator.messenger

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.messenger.HaomaPalette.BG_BASE
import io.haoma.calculator.messenger.HaomaPalette.C_DANGER
import io.haoma.calculator.messenger.HaomaPalette.FG_SECONDARY


@Composable
internal fun DestroyNoticeHost(store: MessengerStore) {
    val sentence by store.destroySentence.collectAsStateWithLifecycle()
    val text = sentence ?: return

    BackHandler(enabled = true) {  }

    Box(
        modifier = Modifier.fillMaxSize().background(BG_BASE),
        contentAlignment = Alignment.Center,
    ) {
        Column(
            modifier = Modifier.padding(horizontal = 32.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Text(
                text = text,
                color = C_DANGER,
                fontSize = 20.sp,
                textAlign = TextAlign.Center,
            )
            Text(
                text = DESTROY_NOTICE_FOOTER,
                color = FG_SECONDARY,
                fontSize = 14.sp,
                textAlign = TextAlign.Center,
            )
        }
    }
}


internal const val DESTROY_NOTICE_FOOTER =
    "This device is erasing what it holds. It cannot be stopped from here."
