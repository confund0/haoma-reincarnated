package io.haoma.calculator.unlock

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.IconButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.core.UnlockManager
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch


@Composable
fun PassphraseScreen(
    unlock: UnlockManager,
    log: (String) -> Unit,
    idleTimeoutMs: Long = IdleTimeoutMs,
    maxStrikes: Int = MaxStrikes,
) {
    var passphrase by remember { mutableStateOf("") }
    var visible by remember { mutableStateOf(false) }
    var submitting by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var strikes by remember { mutableIntStateOf(0) }

    val scope = rememberCoroutineScope()
    val focus = remember { FocusRequester() }

    
    LaunchedEffect(Unit) {
        focus.requestFocus()
    }

    
    LaunchedEffect(passphrase, submitting) {
        if (submitting) return@LaunchedEffect
        delay(idleTimeoutMs)
        log("idle timeout → Hard")
        unlock.revertToHard()
    }

    Box(
        modifier = Modifier
            .fillMaxSize()
            .background(Bg0),
        contentAlignment = Alignment.Center,
    ) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 32.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.Center,
        ) {
            Text(
                text = "Haoma",
                color = Fg,
                fontWeight = FontWeight.Light,
                style = TextStyle(fontSize = 32.sp, letterSpacing = 4.sp),
            )
            Spacer(Modifier.height(40.dp))

            OutlinedTextField(
                value = passphrase,
                onValueChange = { next ->
                    passphrase = next
                    if (error != null) error = null
                },
                modifier = Modifier
                    .fillMaxWidth()
                    .focusRequester(focus),
                enabled = !submitting,
                singleLine = true,
                visualTransformation = if (visible) {
                    VisualTransformation.None
                } else {
                    PassMask
                },
                keyboardOptions = KeyboardOptions(
                    keyboardType = KeyboardType.Password,
                    imeAction = ImeAction.Done,
                    autoCorrectEnabled = false,
                ),
                keyboardActions = KeyboardActions(
                    onDone = {
                        attemptSubmit(
                            scope = scope,
                            unlock = unlock,
                            passphrase = passphrase,
                            log = log,
                            setSubmitting = { submitting = it },
                            onWrong = {
                                strikes += 1
                                error = "Wrong passphrase"
                                passphrase = ""
                                if (strikes >= maxStrikes) {
                                    log("strike limit reached ($maxStrikes) → Hard")
                                    unlock.revertToHard()
                                }
                            },
                            onSpawnFail = { msg -> error = "Spawn failed: $msg" },
                        )
                    },
                ),
                placeholder = { Text("Passphrase", color = Fg2.copy(alpha = 0.5f)) },
                trailingIcon = {
                    IconButton(
                        onClick = { visible = !visible },
                        enabled = !submitting,
                    ) {
                        Text(
                            text = if (visible) "hide" else "show",
                            color = Fg2,
                            style = TextStyle(fontSize = 12.sp),
                        )
                    }
                },
                isError = error != null,
                colors = OutlinedTextFieldDefaults.colors(
                    focusedTextColor = Fg,
                    unfocusedTextColor = Fg,
                    cursorColor = Fg,
                    focusedBorderColor = Accent,
                    unfocusedBorderColor = Fg2,
                    errorBorderColor = ErrorFg,
                    errorTextColor = Fg,
                    errorCursorColor = ErrorFg,
                    disabledTextColor = Fg2,
                    disabledBorderColor = Fg2.copy(alpha = 0.3f),
                ),
            )

            Spacer(Modifier.height(16.dp))

            Box(
                modifier = Modifier.fillMaxWidth(),
                contentAlignment = Alignment.Center,
            ) {
                Button(
                    onClick = {
                        attemptSubmit(
                            scope = scope,
                            unlock = unlock,
                            passphrase = passphrase,
                            log = log,
                            setSubmitting = { submitting = it },
                            onWrong = {
                                strikes += 1
                                error = "Wrong passphrase"
                                passphrase = ""
                                if (strikes >= maxStrikes) {
                                    log("strike limit reached ($maxStrikes) → Hard")
                                    unlock.revertToHard()
                                }
                            },
                            onSpawnFail = { msg -> error = "Spawn failed: $msg" },
                        )
                    },
                    enabled = !submitting && passphrase.isNotEmpty(),
                    colors = ButtonDefaults.buttonColors(
                        containerColor = Bg2,
                        contentColor = Fg,
                        disabledContainerColor = Bg1,
                        disabledContentColor = Fg2.copy(alpha = 0.5f),
                    ),
                ) {
                    Text("Unlock")
                }

                if (submitting) {
                    CircularProgressIndicator(
                        modifier = Modifier.size(20.dp),
                        color = Accent,
                        strokeWidth = 2.dp,
                    )
                }
            }

            Spacer(Modifier.height(8.dp))

            
            TextButton(
                onClick = {
                    attemptDefaultSubmit(
                        scope = scope,
                        unlock = unlock,
                        log = log,
                        setSubmitting = { submitting = it },
                        onWrong = {
                            strikes += 1
                            error = "Default passphrase doesn't apply — type yours"
                            passphrase = ""
                            if (strikes >= maxStrikes) {
                                log("strike limit reached ($maxStrikes) → Hard")
                                unlock.revertToHard()
                            }
                        },
                        onSpawnFail = { msg -> error = "Spawn failed: $msg" },
                    )
                },
                enabled = !submitting,
            ) {
                Text(
                    text = "Use default passphrase",
                    color = if (submitting) Fg2.copy(alpha = 0.5f) else Fg2,
                    style = TextStyle(fontSize = 13.sp),
                )
            }

            Spacer(Modifier.height(8.dp))

            error?.let { msg ->
                Text(
                    text = msg,
                    color = ErrorFg,
                    style = TextStyle(fontSize = 14.sp),
                )
            }
        }
    }
}

private fun attemptSubmit(
    scope: kotlinx.coroutines.CoroutineScope,
    unlock: UnlockManager,
    passphrase: String,
    log: (String) -> Unit,
    setSubmitting: (Boolean) -> Unit,
    onWrong: () -> Unit,
    onSpawnFail: (String) -> Unit,
) {
    if (passphrase.isEmpty()) return
    setSubmitting(true)
    log("submit (len=${passphrase.length})")
    scope.launch {
        try {
            handleOutcome(unlock.submitPassphrase(passphrase), log = log, onWrong = onWrong, onSpawnFail = onSpawnFail)
        } finally {
            setSubmitting(false)
        }
    }
}

private fun attemptDefaultSubmit(
    scope: kotlinx.coroutines.CoroutineScope,
    unlock: UnlockManager,
    log: (String) -> Unit,
    setSubmitting: (Boolean) -> Unit,
    onWrong: () -> Unit,
    onSpawnFail: (String) -> Unit,
) {
    setSubmitting(true)
    log("submit default")
    scope.launch {
        try {
            handleOutcome(unlock.submitDefaultPassphrase(), log = log, onWrong = onWrong, onSpawnFail = onSpawnFail)
        } finally {
            setSubmitting(false)
        }
    }
}

private fun handleOutcome(
    outcome: UnlockManager.Outcome,
    log: (String) -> Unit,
    onWrong: () -> Unit,
    onSpawnFail: (String) -> Unit,
) {
    when (outcome) {
        UnlockManager.Outcome.Warmed -> {
            log("submit → Warm")
            
            
        }
        UnlockManager.Outcome.WrongPassphrase -> {
            log("submit wrong-pass")
            onWrong()
        }
        UnlockManager.Outcome.NeedsPassphrase -> {
            
            
            log("submit unexpected NeedsPassphrase")
            onWrong()
        }
        is UnlockManager.Outcome.SpawnFailed -> {
            log("submit spawn-failed: ${outcome.message}")
            onSpawnFail(outcome.message)
        }
    }
}

private const val IdleTimeoutMs = 5_000L
private const val MaxStrikes = 3


private val Bg0 = Color(0xFF1D2021)
private val Bg1 = Color(0xFF3C3836)
private val Bg2 = Color(0xFF504945)
private val Fg = Color(0xFFEBDBB2)
private val Fg2 = Color(0xFFD5C4A1)
private val Accent = Color(0xFF83A598)
private val ErrorFg = Color(0xFFCC241D)

private val PassMask = PasswordVisualTransformation()
