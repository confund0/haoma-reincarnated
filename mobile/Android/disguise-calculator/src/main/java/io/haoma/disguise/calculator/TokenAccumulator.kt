package io.haoma.disguise.calculator


internal class TokenAccumulator(seed: String) {
    private val keys = mutableListOf(seed)

    
    fun visit(key: String) {
        if (keys.last() == key) return
        keys += key
    }

    
    val token: String get() = keys.drop(1).joinToString("")

    
    val isEmpty: Boolean get() = keys.size <= 1
}
