package io.haoma.calculator.core

import android.content.Context
import io.haoma.calculator.log.Logger


class DisguiseStore(
    private val context: Context,
) {
    

    fun bootstrapIfMissing(factoryDefault: String) {
        
        
        val probe = VaultHelper.disguiseVerify(context, factoryDefault)
        if (probe == VaultHelper.DisguiseVerifyResult.Missing) {
            Logger.i("disguise", "sidecar missing; minting with factory default")
            VaultHelper.disguiseInit(context, factoryDefault)
        }
    }

    
    fun verify(token: String): Boolean {
        if (token.isEmpty()) return false
        val result = VaultHelper.disguiseVerify(context, token)
        Logger.d("disguise", "verify result=$result")
        return when (result) {
            VaultHelper.DisguiseVerifyResult.Match -> true
            VaultHelper.DisguiseVerifyResult.Mismatch -> false
            VaultHelper.DisguiseVerifyResult.Missing -> {
                Logger.w("disguise", "verify: sidecar missing (out-of-band deletion?)")
                false
            }
        }
    }

    
    fun rekey(oldPattern: String, newPattern: String) {
        VaultHelper.disguiseRekey(context, oldPattern, newPattern)
    }
}
