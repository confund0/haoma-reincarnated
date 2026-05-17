package io.haoma.calculator.messenger.calls

import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.haoma.calculator.log.Logger
import io.haoma.calculator.messenger.CallModality
import io.haoma.calculator.messenger.CallStatus
import io.haoma.calculator.messenger.MessengerStore


@Composable
internal fun CallWindowHost(store: MessengerStore) {
    val active by store.activeCalls.collectAsStateWithLifecycle()
    val open by store.callWindowOpen.collectAsStateWithLifecycle()

    
    val focusState = remember(active) {
        derivedStateOf {
            active.values
                .filter {
                    !it.isTerminal &&
                        it.status == CallStatus.Accepted &&
                        CallModality.Video in it.modalities
                }
                .minByOrNull { it.startedAt }
        }
    }
    val focus = focusState.value

    
    LaunchedEffect(focus?.callId) {
        val f = focus
        if (f != null) {
            if (!store.callWindowOpen.value) {
                Logger.i(
                    "call",
                    "callwindow auto-open call=${shortLog(f.callId)}",
                )
            }
            store._callWindowOpen.value = true
        }
    }

    if (open && focus != null) {
        CallWindow(
            call = focus,
            store = store,
            onDismiss = { store._callWindowOpen.value = false },
        )
    }
}

private fun shortLog(callId: String): String =
    if (callId.length <= 8) callId else callId.take(8)
