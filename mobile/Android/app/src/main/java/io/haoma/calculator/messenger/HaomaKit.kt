package io.haoma.calculator.messenger

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.interaction.collectIsFocusedAsState
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.wrapContentHeight
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicText
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp


@Composable
fun Section(
    label: String,
    description: String? = null,
    content: @Composable () -> Unit,
) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 16.dp),
    ) {
        Text(
            text = label.uppercase(),
            color = HaomaPalette.FG_PRIMARY,
            fontSize = 13.sp,
            fontWeight = FontWeight.SemiBold,
        )
        Spacer(modifier = Modifier.height(10.dp))
        content()
        if (description != null) {
            Spacer(modifier = Modifier.height(12.dp))
            Text(
                text = description,
                color = HaomaPalette.FG_SECONDARY,
                fontSize = 13.sp,
                lineHeight = 18.sp,
            )
        }
    }
    HorizontalDivider(color = HaomaPalette.DIVIDER, thickness = 0.5.dp)
}


@Composable
fun CtaButton(
    label: String,
    accent: Color,
    enabled: Boolean = true,
    modifier: Modifier = Modifier,
    onClick: () -> Unit,
) {
    Button(
        enabled = enabled,
        onClick = onClick,
        shape = RoundedCornerShape(8.dp),
        contentPadding = PaddingValues(horizontal = 24.dp, vertical = 10.dp),
        colors = ButtonDefaults.buttonColors(
            containerColor = accent,
            contentColor = HaomaPalette.BG_BASE,
            disabledContainerColor = HaomaPalette.BTN_DIM,
            disabledContentColor = HaomaPalette.FG_DIM,
        ),
        modifier = modifier,
    ) {
        Text(label, fontSize = 13.sp, fontWeight = FontWeight.SemiBold)
    }
}


@Composable
fun DangerButton(
    label: String,
    enabled: Boolean = true,
    modifier: Modifier = Modifier,
    onClick: () -> Unit,
) {
    OutlinedButton(
        enabled = enabled,
        onClick = onClick,
        shape = RoundedCornerShape(8.dp),
        contentPadding = PaddingValues(horizontal = 24.dp, vertical = 10.dp),
        colors = ButtonDefaults.outlinedButtonColors(
            contentColor = HaomaPalette.C_DANGER,
            disabledContentColor = HaomaPalette.FG_DIM,
        ),
        border = BorderStroke(
            width = 1.5.dp,
            color = if (enabled) HaomaPalette.C_DANGER else HaomaPalette.BTN_DIM,
        ),
        modifier = modifier,
    ) {
        Text(label, fontSize = 13.sp, fontWeight = FontWeight.SemiBold)
    }
}


@Composable
fun haomaTextFieldColors() = OutlinedTextFieldDefaults.colors(
    focusedTextColor = HaomaPalette.FG_PRIMARY,
    unfocusedTextColor = HaomaPalette.FG_PRIMARY,
    disabledTextColor = HaomaPalette.FG_DIM,
    focusedContainerColor = HaomaPalette.BG_FIELD,
    unfocusedContainerColor = HaomaPalette.BG_FIELD,
    disabledContainerColor = HaomaPalette.BG_FIELD,
    cursorColor = HaomaPalette.FG_LINK,
    focusedBorderColor = HaomaPalette.FG_LINK,
    unfocusedBorderColor = Color.Transparent,
    disabledBorderColor = Color.Transparent,
)


@Composable
fun DirtyBar(
    visible: Boolean,
    onTap: () -> Unit,
    modifier: Modifier = Modifier,
) {
    AnimatedVisibility(
        visible = visible,
        enter = fadeIn(),
        exit = fadeOut(),
        modifier = modifier,
    ) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .background(HaomaPalette.C_ATTENTION)
                .clickable(onClick = onTap)
                .padding(horizontal = 16.dp, vertical = 10.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(
                text = "Unsaved changes — tap to review",
                color = HaomaPalette.BG_BASE,
                fontSize = 13.sp,
                fontWeight = FontWeight.SemiBold,
                modifier = Modifier.weight(1f),
            )
            Text(
                text = "›",
                color = HaomaPalette.BG_BASE,
                fontSize = 18.sp,
                fontWeight = FontWeight.Bold,
            )
        }
    }
}


@Composable
fun HaomaTextField(
    value: String,
    onValueChange: (String) -> Unit,
    modifier: Modifier = Modifier,
    placeholder: String = "",
    singleLine: Boolean = true,
    keyboardOptions: KeyboardOptions = KeyboardOptions.Default,
) {
    val interactionSource = remember { MutableInteractionSource() }
    val focused by interactionSource.collectIsFocusedAsState()
    Surface(
        modifier = modifier.heightIn(min = 46.dp),
        shape = RoundedCornerShape(10.dp),
        color = HaomaPalette.BG_FIELD,
        border = BorderStroke(
            width = 1.dp,
            color = if (focused) HaomaPalette.FG_LINK else Color.Transparent,
        ),
    ) {
        BasicTextField(
            value = value,
            onValueChange = onValueChange,
            singleLine = singleLine,
            interactionSource = interactionSource,
            keyboardOptions = keyboardOptions,
            modifier = Modifier
                .fillMaxWidth()
                .wrapContentHeight(Alignment.CenterVertically)
                .padding(horizontal = 14.dp, vertical = 8.dp),
            textStyle = TextStyle(color = HaomaPalette.BTN_GIVE, fontSize = 14.sp),
            cursorBrush = SolidColor(HaomaPalette.FG_LINK),
            decorationBox = { inner ->
                Box(modifier = Modifier.fillMaxWidth()) {
                    if (value.isEmpty() && placeholder.isNotEmpty()) {
                        BasicText(
                            text = placeholder,
                            style = TextStyle(
                                color = HaomaPalette.FG_DIM,
                                fontSize = 14.sp,
                            ),
                        )
                    }
                    inner()
                }
            },
        )
    }
}


@Composable
fun HaomaDropdownItem(
    label: String,
    selected: Boolean,
    onClick: () -> Unit,
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onClick)
            .padding(horizontal = 12.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = label,
            color = if (selected) HaomaPalette.FG_LINK else HaomaPalette.FG_PRIMARY,
            fontSize = 14.sp,
        )
    }
}
