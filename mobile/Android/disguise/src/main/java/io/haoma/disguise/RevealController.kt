package io.haoma.disguise


interface RevealController {
    
    fun arm()

    
    fun submit(token: Any)

    
    fun cancel()
}


class LoggingRevealController(
    private val log: (String) -> Unit,
) : RevealController {
    override fun arm() = log("arm")
    override fun submit(token: Any) = log("submit token=$token")
    override fun cancel() = log("cancel")
}
