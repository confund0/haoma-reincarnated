package io.haoma.calculator.unlock

import io.haoma.calculator.core.UnlockManager
import io.haoma.disguise.AppState
import io.haoma.disguise.AppStateRepository
import io.haoma.disguise.RevealController


class VerifyingRevealController(
    private val state: AppStateRepository,
    private val config: PatternConfig,
    private val unlock: UnlockManager,
    private val log: (String) -> Unit,
) : RevealController {
    override fun arm() = log("arm")

    override fun cancel() = log("cancel")

    override fun submit(token: Any) {
        if (!config.verify(token.toString())) {
            log("submit mismatch")
            return
        }
        when (state.state.value) {
            AppState.Locked.Soft -> {
                log("submit match soft→warm")
                state.update(AppState.Warm)
            }
            AppState.Locked.Hard -> {
                log("submit match hard→unseal")
                unlock.handleHardSlideMatch()
            }
            AppState.Locked.Safe -> {
                log("submit match safe→unseal")
                unlock.handleHardSlideMatch()
            }
            AppState.Locked.Passphrase, AppState.Warm -> {
                log("submit match no-op (already unlocked)")
            }
        }
    }
}
