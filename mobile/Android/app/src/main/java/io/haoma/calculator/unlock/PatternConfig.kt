package io.haoma.calculator.unlock

import io.haoma.calculator.core.DisguiseStore


class PatternConfig(
    private val disguise: DisguiseStore,
) {
    

    fun verify(token: String): Boolean = disguise.verify(token)

    companion object {
        const val FACTORY_DEFAULT = "78963"
    }
}
