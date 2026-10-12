package io.haoma.calculator.core

import android.content.Context
import io.haoma.calculator.log.Logger
import java.io.File
import java.util.concurrent.TimeUnit
import org.json.JSONObject


object VaultHelper {
    
    const val DefaultPassphrase = "good-girls-go-to-heaven"

    
    val DefaultPassphraseBytes: ByteArray = DefaultPassphrase.toByteArray(Charsets.UTF_8)

    private const val ExecTimeoutSec = 30L
    private const val LibName = "libhaoma-vault.so"

    
    data class Unsealed(
        val secrets: ByteArray,
        val policy: IdlePolicy,
        val payload: ByteArray,
    )

    
    fun cfgDir(context: Context): File = File(context.filesDir, "haoma")

    
    fun unseal(context: Context, passphrase: ByteArray = DefaultPassphraseBytes): Unsealed {
        Logger.d("vault", "unseal enter empty=${passphrase.isEmpty()}")
        val (stdout, exit) = execHelperAllowExit(
            context,
            args = emptyArray(),
            stdin = passphrase,
            allowed = setOf(0, RESTORE_PENDING_EXIT),
        )
        if (exit == RESTORE_PENDING_EXIT) throw RestorePendingException()
        return splitOutput(stdout)
    }

    
    const val RESTORE_PENDING_EXIT = 7

    
    class RestorePendingException : IllegalStateException("a restore did not finish")

    
    const val NOT_EMPTY_EXIT = 8

    
    class NotEmptyException : IllegalStateException(RESTORE_NOT_EMPTY)

    
    const val RESTORE_NOT_EMPTY =
        "This device holds contacts or chats. A backup can only be restored on an empty device. " +
            "Unlock the app and wipe it, or reinstall the app, then restore. " +
            "If anything on this device is valuable, make a backup of it first."

    
    const val RESTORE_DID_NOT_FINISH =
        "The restore did not finish, so this device cannot be opened. " +
            "Uninstall the app, install it again, then restore your backup."

    
    const val RESTORE_MARKER = ".restore-commit"

    
    fun pendingRestoreMessage(context: Context): String? = pendingRestoreMessage(File(cfgDir(context), RESTORE_MARKER))

    
    internal fun pendingRestoreMessage(marker: File): String? =
        if (marker.exists()) RESTORE_DID_NOT_FINISH else null

    
    enum class RestoreEntry { Allowed, NotEmpty, Pending }

    
    fun checkRestoreAllowed(context: Context): RestoreEntry {
        val (_, exit) = execHelperAllowExit(
            context,
            args = arrayOf("--check-empty"),
            stdin = ByteArray(0),
            allowed = setOf(0, NOT_EMPTY_EXIT, RESTORE_PENDING_EXIT),
        )
        Logger.i("vault", "check-empty exit=$exit")
        return restoreEntryFor(exit)
    }

    
    internal fun restoreEntryFor(exit: Int): RestoreEntry = when (exit) {
        0 -> RestoreEntry.Allowed
        RESTORE_PENDING_EXIT -> RestoreEntry.Pending
        else -> RestoreEntry.NotEmpty
    }

    
    fun reseal(context: Context, payloadJson: ByteArray, passphrase: ByteArray) {
        Logger.d("vault", "reseal enter empty=${passphrase.isEmpty()} payload_bytes=${payloadJson.size}")
        val stdin = ByteArray(passphrase.size + 1 + payloadJson.size)
        try {
            System.arraycopy(passphrase, 0, stdin, 0, passphrase.size)
            stdin[passphrase.size] = '\n'.code.toByte()
            System.arraycopy(payloadJson, 0, stdin, passphrase.size + 1, payloadJson.size)
            execHelper(context, args = arrayOf("-w"), stdin = stdin)
        } finally {
            
            
            java.util.Arrays.fill(stdin, 0)
        }
        Logger.i("vault", "reseal ok payload_bytes=${payloadJson.size}")
    }

    
    enum class DisguiseVerifyResult { Match, Mismatch, Missing }

    
    fun archiveWrite(context: Context, destPath: String) {
        execHelper(context, args = arrayOf("--archive-write=$destPath"), stdin = ByteArray(0))
        Logger.i("vault", "archive-write ok bytes=${File(destPath).length()}")
    }

    
    fun archiveStage(context: Context, archivePath: String): String {
        val (stdout, exit) = execHelperAllowExit(
            context,
            args = arrayOf("--archive-stage=$archivePath"),
            stdin = ByteArray(0),
            allowed = setOf(0, NOT_EMPTY_EXIT, RESTORE_PENDING_EXIT),
        )
        if (exit == NOT_EMPTY_EXIT) throw NotEmptyException()
        if (exit == RESTORE_PENDING_EXIT) throw RestorePendingException()
        val staging = stdout.toString(Charsets.UTF_8).trim()
        require(staging.isNotEmpty()) { "archive-stage produced empty stdout" }
        Logger.i("vault", "archive-stage ok staging=$staging")
        return staging
    }

    
    sealed interface CommitResult {
        
        data class Swapped(val unsealed: Unsealed) : CommitResult

        
        data object WrongPassphrase : CommitResult

        
        data object NotApplied : CommitResult

        
        data object Broken : CommitResult

        
        data object StoreInUse : CommitResult

        
        data object SwappedNoBundle : CommitResult

        
        data object Pending : CommitResult

        
        data object NotEmpty : CommitResult
    }

    
    fun archiveCommit(context: Context, stagingPath: String, passphrase: ByteArray): CommitResult {
        Logger.d("vault", "archive-commit enter empty=${passphrase.isEmpty()} staging=$stagingPath")
        val (stdout, exit) = execHelperAllowExit(
            context,
            args = arrayOf("--archive-commit=$stagingPath"),
            stdin = passphrase,
            allowed = setOf(0, 1, 3, 4, 5, 6, RESTORE_PENDING_EXIT, NOT_EMPTY_EXIT),
        )
        Logger.i("vault", "archive-commit exit=$exit")
        return commitResultFor(exit) { splitOutput(stdout) }
    }

    
    internal fun commitResultFor(exit: Int, unsealed: () -> Unsealed): CommitResult = when (exit) {
        0 -> CommitResult.Swapped(unsealed())
        1 -> CommitResult.WrongPassphrase
        3 -> CommitResult.NotApplied
        5 -> CommitResult.StoreInUse
        6 -> CommitResult.SwappedNoBundle
        RESTORE_PENDING_EXIT -> CommitResult.Pending
        NOT_EMPTY_EXIT -> CommitResult.NotEmpty
        else -> CommitResult.Broken
    }

    
    fun archiveDiscard(context: Context, stagingPath: String) {
        execHelper(context, args = arrayOf("--archive-discard=$stagingPath"), stdin = ByteArray(0))
        Logger.i("vault", "archive-discard ok staging=$stagingPath")
    }

    
    fun cleanRestoreLeftovers(context: Context): Boolean {
        val (_, exit) = execHelperAllowExit(
            context,
            args = arrayOf("--clean-restore-leftovers"),
            stdin = ByteArray(0),
            allowed = setOf(0, RESTORE_PENDING_EXIT),
        )
        Logger.i("vault", "clean-restore-leftovers exit=$exit")
        return exit == 0
    }

    
    fun disguiseInit(context: Context, pattern: String) {
        execHelper(context, args = arrayOf("--disguise-init"), stdin = pattern.toByteArray(Charsets.UTF_8))
        Logger.i("vault", "disguise-init ok")
    }

    
    fun disguiseVerify(context: Context, pattern: String): DisguiseVerifyResult {
        val (_, exit) = execHelperAllowExit(
            context,
            args = arrayOf("--disguise-verify"),
            stdin = pattern.toByteArray(Charsets.UTF_8),
            allowed = setOf(0, 1, 2),
        )
        return when (exit) {
            0 -> DisguiseVerifyResult.Match
            2 -> DisguiseVerifyResult.Missing
            else -> DisguiseVerifyResult.Mismatch
        }
    }

    
    fun disguiseRekey(context: Context, oldPattern: String, newPattern: String) {
        val oldBytes = oldPattern.toByteArray(Charsets.UTF_8)
        val newBytes = newPattern.toByteArray(Charsets.UTF_8)
        val stdin = ByteArray(oldBytes.size + 1 + newBytes.size)
        System.arraycopy(oldBytes, 0, stdin, 0, oldBytes.size)
        stdin[oldBytes.size] = '\n'.code.toByte()
        System.arraycopy(newBytes, 0, stdin, oldBytes.size + 1, newBytes.size)
        execHelper(context, args = arrayOf("--disguise-rekey"), stdin = stdin)
        Logger.i("vault", "disguise-rekey ok")
    }

    
    private fun execHelper(
        context: Context,
        args: Array<String>,
        stdin: ByteArray,
    ): ByteArray {
        val (stdout, _) = execHelperAllowExit(context, args, stdin, setOf(0))
        return stdout
    }

    
    private fun execHelperAllowExit(
        context: Context,
        args: Array<String>,
        stdin: ByteArray,
        allowed: Set<Int>,
    ): Pair<ByteArray, Int> {
        val cfg = cfgDir(context)
        val bin = File(context.applicationInfo.nativeLibraryDir, LibName)
        require(bin.exists()) { "$LibName missing at ${bin.absolutePath}" }

        val errLog = File(Logger.fileFor("haoma-vault"))
        val argsList = mutableListOf(bin.absolutePath, "--cfg-dir", cfg.absolutePath)
        argsList.addAll(args)
        Logger.i("vault", "exec ${argsList.joinToString(" ")}")

        val proc = ProcessBuilder(argsList)
            .redirectError(ProcessBuilder.Redirect.appendTo(errLog))
            .start()

        proc.outputStream.use { it.write(stdin) }
        val stdout = proc.inputStream.use { it.readBytes() }

        val finished = proc.waitFor(ExecTimeoutSec, TimeUnit.SECONDS)
        if (!finished) {
            proc.destroyForcibly()
            error("haoma-vault timed out after ${ExecTimeoutSec}s")
        }
        val exit = proc.exitValue()
        if (exit !in allowed) {
            error("haoma-vault exit=$exit; see ${errLog.absolutePath}")
        }
        return stdout to exit
    }

    
    internal fun splitOutput(stdout: ByteArray): Unsealed {
        val (secrets, afterSecrets) = splitOnce(stdout) ?: run {
            Logger.w("vault", "secrets line missing; whole stdout treated as secrets")
            return Unsealed(stdout.copyOf(), IdlePolicy.Default, ByteArray(0))
        }
        val payload = trimTrailingNewline(afterSecrets)
        if (payload.isEmpty()) {
            Logger.w("vault", "payload line missing; using IdlePolicy defaults + empty payload")
            return Unsealed(secrets, IdlePolicy.Default, ByteArray(0))
        }
        val policy = parsePolicy(payload)
        Logger.i(
            "vault",
            "unseal ok secrets=${secrets.size}B policy=${policy.action}/${policy.timeoutSeconds}s payload=${payload.size}B",
        )
        return Unsealed(secrets, policy, payload)
    }

    private fun splitOnce(buf: ByteArray): Pair<ByteArray, ByteArray>? {
        val nl = buf.indexOf('\n'.code.toByte())
        if (nl < 0) return null
        return buf.copyOfRange(0, nl) to buf.copyOfRange(nl + 1, buf.size)
    }

    private fun trimTrailingNewline(buf: ByteArray): ByteArray {
        var end = buf.size
        while (end > 0 && (buf[end - 1] == '\n'.code.toByte() || buf[end - 1] == '\r'.code.toByte())) {
            end--
        }
        return buf.copyOfRange(0, end)
    }

    
    private fun parsePolicy(payload: ByteArray): IdlePolicy {
        val text = payload.toString(Charsets.UTF_8).trim()
        if (text.isEmpty()) {
            Logger.w("vault", "payload empty; using IdlePolicy defaults")
            return IdlePolicy.Default
        }
        return try {
            IdlePolicy.fromJson(JSONObject(text))
        } catch (t: Throwable) {
            Logger.e("vault", "payload parse for policy failed; using defaults", t)
            IdlePolicy.Default
        }
    }

}
