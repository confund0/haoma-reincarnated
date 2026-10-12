package io.haoma.calculator.messenger.settings

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.ime
import androidx.compose.foundation.relocation.BringIntoViewRequester
import androidx.compose.foundation.relocation.bringIntoViewRequester
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LifecycleEventEffect
import io.haoma.calculator.messenger.DangerButton
import io.haoma.calculator.messenger.HaomaPalette
import io.haoma.calculator.messenger.MessengerStore
import io.haoma.calculator.messenger.Section
import io.haoma.calculator.messenger.WIPE_ACTION
import io.haoma.calculator.messenger.WIPE_ASKING
import io.haoma.calculator.messenger.WIPE_BLOCK_LABEL
import io.haoma.calculator.messenger.WIPE_BODY_GOES
import io.haoma.calculator.messenger.WIPE_BODY_ITEMS
import io.haoma.calculator.messenger.WIPE_BODY_NO_UNDO
import io.haoma.calculator.messenger.WIPE_BODY_SAVED_FILES
import io.haoma.calculator.messenger.WIPE_BODY_STAYS
import io.haoma.calculator.messenger.WIPE_FIELD_UNLOCK
import io.haoma.calculator.messenger.WIPE_FIELD_WORD
import io.haoma.calculator.messenger.WIPE_HINT_BYPASS
import io.haoma.calculator.messenger.WIPE_HINT_UNLOCK
import io.haoma.calculator.messenger.loadUnlockKeys
import io.haoma.calculator.messenger.wipeThisDevice
import kotlinx.coroutines.launch


@OptIn(ExperimentalFoundationApi::class)
@Composable
internal fun WipeDeviceBlock(store: MessengerStore) {
    var unlockAnswer by remember { mutableStateOf("") }
    var word by remember { mutableStateOf("") }
    var note by remember { mutableStateOf<String?>(null) }
    var working by remember { mutableStateOf(false) }
    val coroutineScope = rememberCoroutineScope()

    
    val fields = remember { BringIntoViewRequester() }
    var fieldFocused by remember { mutableStateOf(false) }
    val keyboardHeight = WindowInsets.ime.getBottom(LocalDensity.current)
    LaunchedEffect(fieldFocused, keyboardHeight) {
        if (fieldFocused && keyboardHeight > 0) fields.bringIntoView()
    }

    
    LifecycleEventEffect(Lifecycle.Event.ON_STOP) {
        unlockAnswer = ""
        word = ""
    }

    
    val bypassKey = remember { store.loadUnlockKeys().bypassKey }

    Section(label = WIPE_BLOCK_LABEL) {
        Text(
            text = WIPE_BODY_GOES,
            color = HaomaPalette.C_DANGER,
            fontSize = 14.sp,
        )
        Spacer(modifier = Modifier.height(4.dp))
        Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
            WIPE_BODY_ITEMS.forEach { item ->
                Text(text = "- $item", color = HaomaPalette.FG_PRIMARY, fontSize = 14.sp)
            }
        }
        Spacer(modifier = Modifier.height(6.dp))
        Text(text = WIPE_BODY_NO_UNDO, color = HaomaPalette.C_DANGER, fontSize = 14.sp)
        Spacer(modifier = Modifier.height(10.dp))
        Text(text = WIPE_BODY_STAYS, color = HaomaPalette.FG_SECONDARY, fontSize = 13.sp)
        Spacer(modifier = Modifier.height(4.dp))
        Text(text = WIPE_BODY_SAVED_FILES, color = HaomaPalette.FG_DIM, fontSize = 13.sp)

        Spacer(modifier = Modifier.height(14.dp))

        Column(modifier = Modifier.bringIntoViewRequester(fields)) {
            OutlinedTextField(
                value = unlockAnswer,
                onValueChange = { unlockAnswer = it; note = null },
                enabled = !working,
                singleLine = true,
                visualTransformation = PasswordVisualTransformation(),
                keyboardOptions = KeyboardOptions(
                    keyboardType = KeyboardType.Password,
                    imeAction = ImeAction.Next,
                ),
                label = { Text(text = WIPE_FIELD_UNLOCK, color = HaomaPalette.FG_DIM, fontSize = 13.sp) },
                colors = dialogFieldColors(),
                shape = RoundedCornerShape(10.dp),
                modifier = Modifier
                    .fillMaxWidth()
                    .onFocusChanged { if (it.isFocused) fieldFocused = true },
            )
            Spacer(modifier = Modifier.height(4.dp))
            Text(
                text = if (bypassKey.isEmpty()) WIPE_HINT_UNLOCK else WIPE_HINT_BYPASS,
                color = HaomaPalette.FG_DIM,
                fontSize = 12.sp,
            )

            Spacer(modifier = Modifier.height(10.dp))

            OutlinedTextField(
                value = word,
                onValueChange = { word = it; note = null },
                enabled = !working,
                singleLine = true,
                
                
                keyboardOptions = KeyboardOptions(
                    capitalization = KeyboardCapitalization.Characters,
                    autoCorrectEnabled = false,
                    imeAction = ImeAction.Done,
                ),
                label = { Text(text = WIPE_FIELD_WORD, color = HaomaPalette.FG_DIM, fontSize = 13.sp) },
                colors = dialogFieldColors(),
                shape = RoundedCornerShape(10.dp),
                modifier = Modifier
                    .fillMaxWidth()
                    .onFocusChanged { if (it.isFocused) fieldFocused = true },
            )

            Spacer(modifier = Modifier.height(12.dp))

            DangerButton(label = WIPE_ACTION, enabled = !working) {
                working = true
                
                
                note = WIPE_ASKING
                coroutineScope.launch {
                    val result = store.wipeThisDevice(word = word, unlockAnswer = unlockAnswer)
                    note = result.text
                    if (!result.ok) {
                        
                        
                        unlockAnswer = ""
                        word = ""
                        working = false
                    }
                    
                    
                }
            }

            note?.let { sentence ->
                Spacer(modifier = Modifier.height(10.dp))
                Text(text = sentence, color = HaomaPalette.C_DANGER, fontSize = 13.sp)
            }
        }
    }
}
