package io.haoma.disguise

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow


class AppStateRepository(initial: AppState = AppState.Locked.Hard) {
    private val _state = MutableStateFlow(initial)
    val state: StateFlow<AppState> = _state.asStateFlow()

    fun update(next: AppState) {
        _state.value = next
    }
}
