package io.haoma.calculator.unlock

import android.content.Context
import android.net.Uri
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
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
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
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
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.haoma.calculator.HaomaApp
import io.haoma.calculator.core.StagedRestoreState
import io.haoma.calculator.core.UnlockManager
import io.haoma.calculator.messenger.HaomaPalette
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch


@Composable
fun PassphraseScreen(
    unlock: UnlockManager,
    log: (String) -> Unit,
    stagedRestore: StagedRestoreState? = null,
    onStagedRestoreSet: (StagedRestoreState?) -> Unit = {},
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
    val context = LocalContext.current

    val staged = stagedRestore

    val restoreLauncher = rememberLauncherForActivityResult(
        contract = remember { ActivityResultContracts.OpenDocument() },
    ) { uri ->
        if (uri == null) {
            log("restore picker cancelled")
            return@rememberLauncherForActivityResult
        }
        
        
        val app = context.applicationContext as HaomaApp
        submitting = true
        error = null
        passphrase = ""
        app.launchStagedRestore(
            archiveUri = uri,
            log = log,
            onError = { msg -> error = msg },
            onComplete = { submitting = false },
        )
    }

    
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

            if (staged != null) {
                Spacer(Modifier.height(20.dp))
                StagedRestoreBanner(
                    onDiscard = {
                        val app = context.applicationContext as HaomaApp
                        submitting = true
                        error = null
                        passphrase = ""
                        app.launchStagedRestoreDiscard(
                            stagingPath = staged.stagingPath,
                            log = log,
                            onComplete = { submitting = false },
                        )
                    },
                    enabled = !submitting,
                )
                Spacer(Modifier.height(20.dp))
            } else {
                Spacer(Modifier.height(40.dp))
            }

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
                            staged = staged,
                            log = log,
                            setSubmitting = { submitting = it },
                            onWrong = {
                                strikes += 1
                                error = if (staged != null) {
                                    "Wrong source-device passphrase"
                                } else {
                                    "Wrong passphrase"
                                }
                                passphrase = ""
                                if (strikes >= maxStrikes) {
                                    log("strike limit reached ($maxStrikes) → Hard")
                                    unlock.revertToHard()
                                }
                            },
                            onWarmed = {
                                if (staged != null) onStagedRestoreSet(null)
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
                        modifier = Modifier.padding(end = 6.dp),
                    ) {
                        Icon(
                            
                            
                            imageVector = if (visible) EyeOpenVector else EyeOffVector,
                            contentDescription = if (visible) "Hide passphrase" else "Show passphrase",
                            tint = Fg2,
                            modifier = Modifier.size(20.dp),
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
                            staged = staged,
                            log = log,
                            setSubmitting = { submitting = it },
                            onWrong = {
                                strikes += 1
                                error = if (staged != null) {
                                    "Wrong source-device passphrase"
                                } else {
                                    "Wrong passphrase"
                                }
                                passphrase = ""
                                if (strikes >= maxStrikes) {
                                    log("strike limit reached ($maxStrikes) → Hard")
                                    unlock.revertToHard()
                                }
                            },
                            onWarmed = {
                                if (staged != null) onStagedRestoreSet(null)
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

            if (staged == null) {
                
                
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

                TextButton(
                    onClick = { restoreLauncher.launch(arrayOf("*/*")) },
                    enabled = !submitting,
                ) {
                    Text(
                        text = "Restore from backup",
                        color = if (submitting) Fg2.copy(alpha = 0.5f) else Fg2,
                        style = TextStyle(fontSize = 13.sp),
                    )
                }
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
    scope: CoroutineScope,
    unlock: UnlockManager,
    passphrase: String,
    staged: StagedRestoreState?,
    log: (String) -> Unit,
    setSubmitting: (Boolean) -> Unit,
    onWrong: () -> Unit,
    onWarmed: () -> Unit,
    onSpawnFail: (String) -> Unit,
) {
    if (passphrase.isEmpty()) return
    setSubmitting(true)
    log("submit (len=${passphrase.length} staged=${staged != null})")
    
    
    val passBytes = passphrase.toByteArray(Charsets.UTF_8)
    scope.launch {
        try {
            val outcome = if (staged != null) {
                unlock.submitStagedRestoreCommit(passBytes, staged.stagingPath)
            } else {
                unlock.submitPassphrase(passBytes)
            }
            handleOutcome(outcome, log = log, onWrong = onWrong, onWarmed = onWarmed, onSpawnFail = onSpawnFail)
        } finally {
            java.util.Arrays.fill(passBytes, 0)
            setSubmitting(false)
        }
    }
}

private fun attemptDefaultSubmit(
    scope: CoroutineScope,
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
            handleOutcome(
                unlock.submitDefaultPassphrase(),
                log = log,
                onWrong = onWrong,
                onWarmed = {},
                onSpawnFail = onSpawnFail,
            )
        } finally {
            setSubmitting(false)
        }
    }
}

private fun handleOutcome(
    outcome: UnlockManager.Outcome,
    log: (String) -> Unit,
    onWrong: () -> Unit,
    onWarmed: () -> Unit,
    onSpawnFail: (String) -> Unit,
) {
    when (outcome) {
        UnlockManager.Outcome.Warmed -> {
            log("submit → Warm")
            onWarmed()
            
            
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


@Composable
private fun StagedRestoreBanner(onDiscard: () -> Unit, enabled: Boolean) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .background(color = Bg1, shape = RoundedCornerShape(8.dp))
            .padding(horizontal = 14.dp, vertical = 12.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.spacedBy(6.dp),
    ) {
        Text(
            text = "Restoring from backup",
            color = Accent,
            style = TextStyle(fontSize = 16.sp, fontWeight = FontWeight.SemiBold),
        )
        Text(
            text = "Enter the passphrase from the source device to apply the restore.",
            color = Fg2,
            style = TextStyle(fontSize = 14.sp),
        )
        TextButton(onClick = onDiscard, enabled = enabled) {
            Text(
                text = "Discard staged restore",
                color = if (enabled) HaomaPalette.BTN_GIVE else HaomaPalette.BTN_GIVE.copy(alpha = 0.5f),
                style = TextStyle(fontSize = 14.sp),
            )
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
