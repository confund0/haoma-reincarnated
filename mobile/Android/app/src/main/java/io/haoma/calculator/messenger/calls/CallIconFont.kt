package io.haoma.calculator.messenger.calls

import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.text.font.Font
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import io.haoma.calculator.R


object CallIcons {
    
    
    const val Headphones = "\uF025"          
    const val VolumeOff = "\uF026"           
    const val VolumeDown = "\uF027"          
    const val VolumeUp = "\uF028"            
    const val Microphone = "\uF130"          
    const val MicrophoneSlash = "\uF131"     
    const val Headset = "\uF590"             
    const val Bluetooth = "\uF294"           
}

@Composable
fun fontAwesomeSolid(): FontFamily = remember {
    FontFamily(Font(R.font.fa_solid, FontWeight.Black))
}

@Composable
fun fontAwesomeBrands(): FontFamily = remember {
    FontFamily(Font(R.font.fa_brands, FontWeight.Normal))
}


internal fun isBrandsGlyph(glyph: String): Boolean = glyph == CallIcons.Bluetooth
