package io.haoma.disguise.calculator


data class RevealConfig(
    
    val triggerKey: String = "5",
    
    val holdMillis: Long = 2_000,
    

    val armWindowMillis: Long = 5_000,
    

    val slideHitRadiusFraction: Float = 0.4f,
)
