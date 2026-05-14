package io.haoma.disguise.calculator


data class RevealConfig(
    
    val triggerKey: String = "5",
    
    val holdMillis: Long = 2_000,
    

    val armWindowMillis: Long = 5_000,
    

    val slideHitRadiusFraction: Float = 0.4f,

    
    val pinTriggerKey: String = "1",
    
    val pinHoldMillis: Long = 2_000,
    
    val pinSubmitKey: String = "=",
    
    val pinSubmitHoldMillis: Long = 2_000,
    
    val pinIdleCancelMillis: Long = 5_000,
)
