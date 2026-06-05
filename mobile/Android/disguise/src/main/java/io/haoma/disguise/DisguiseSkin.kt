package io.haoma.disguise

import androidx.annotation.DrawableRes
import androidx.compose.runtime.Composable


interface DisguiseSkin {
    
    val id: String

    
    @Composable
    fun Surface(
        reveal: RevealController,
        pendingTip: DisguiseTip?,
        onTipDismissed: () -> Unit,
    )

    
    fun nextTip(): DisguiseTip

    
    @get:DrawableRes
    val notificationIconRes: Int
}


data class DisguiseTip(
    val title: String,
    val body: String,
)
