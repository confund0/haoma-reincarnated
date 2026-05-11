package io.haoma.disguise


sealed class AppState {
    sealed class Locked : AppState() {
        data object Soft : Locked()
        data object Safe : Locked()
        data object Hard : Locked()

        
        data object Passphrase : Locked()
    }

    data object Warm : AppState()
}
