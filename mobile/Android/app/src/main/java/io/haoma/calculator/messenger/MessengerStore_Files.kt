package io.haoma.calculator.messenger

import android.net.Uri
import io.haoma.calculator.core.ipc.FrameType
import io.haoma.calculator.saf.SafBridge
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext


fun MessengerStore.attachFromUri(chatId: String, uri: Uri) {
    if (chatId.isEmpty()) return
    val ctx = appContext ?: run {
        appendStatus("attach: no app context", level = StatusLevel.WARN)
        return
    }
    val peerId = _chats.value.firstOrNull { it.chatId == chatId }?.peerId.orEmpty()
    if (peerId.isEmpty()) {
        appendStatus("attach: chat not found ($chatId)", level = StatusLevel.WARN)
        return
    }
    scope.launch {
        val c = ipc ?: run {
            appendStatus("attach: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        val copied = try {
            withContext(Dispatchers.IO) { SafBridge.copyUriToCache(ctx, uri) }
        } catch (t: Throwable) {
            appendStatus("attach copy failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.SendFile,
                payload = SendFileRequest(peerId, copied.path).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("attach error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
            } else {
                val resp = reply.payload?.let(SendFileResponse::fromJson)
                appendStatus(
                    "attach ${copied.displayName} (${humanBytes(resp?.size ?: 0L)})",
                    level = StatusLevel.INFO,
                )
                if (resp?.envelopeId?.isNotEmpty() == true) {
                    rememberEnvelope(resp.envelopeId, chatId)
                }
            }
        } catch (t: Throwable) {
            appendStatus("attach failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        } finally {
            withContext(Dispatchers.IO) { SafBridge.deleteCacheCopy(copied.path) }
        }
    }
}


suspend fun MessengerStore.listFilesFor(chatId: String): List<FileEntry> {
    if (chatId.isEmpty()) return emptyList()
    val c = ipc ?: run {
        appendStatus("files: ipc not connected", level = StatusLevel.WARN)
        return emptyList()
    }
    return try {
        val reply = c.request(
            type = FrameType.ListFiles,
            payload = ListFilesRequest(chatId).toJson(),
        )
        if (reply.type == FrameType.Error) {
            val err = reply.payload?.let(ErrorPayload::fromJson)
            appendStatus("files error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
            emptyList()
        } else {
            reply.payload?.let(FilesListResponse::fromJson)?.files.orEmpty()
        }
    } catch (t: Throwable) {
        appendStatus("files failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        emptyList()
    }
}


suspend fun MessengerStore.saveFileToUri(chatId: String, msgId: String, destUri: Uri): Boolean {
    if (chatId.isEmpty() || msgId.isEmpty()) return false
    val ctx = appContext ?: run {
        appendStatus("save: no app context", level = StatusLevel.WARN)
        return false
    }
    val c = ipc ?: run {
        appendStatus("save: ipc not connected", level = StatusLevel.WARN)
        return false
    }
    val tempDir = withContext(Dispatchers.IO) { SafBridge.saveOutDir(ctx) }
    return try {
        val reply = c.request(
            type = FrameType.SaveFile,
            payload = SaveFileRequest(chatId, msgId, tempDir.absolutePath).toJson(),
        )
        if (reply.type == FrameType.Error) {
            val err = reply.payload?.let(ErrorPayload::fromJson)
            appendStatus("save error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
            return false
        }
        val resp = reply.payload?.let(SaveFileResponse::fromJson)
        val tempPath = resp?.fullPath.orEmpty()
        if (tempPath.isEmpty()) {
            appendStatus("save: empty path from daemon", level = StatusLevel.WARN)
            return false
        }
        val bytes = withContext(Dispatchers.IO) {
            SafBridge.copyDaemonOutputToUri(ctx, tempPath, destUri)
        }
        appendStatus("saved (${humanBytes(bytes)})", level = StatusLevel.INFO)
        true
    } catch (t: Throwable) {
        appendStatus("save failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        false
    }
}


suspend fun MessengerStore.openFile(chatId: String, msgId: String): OpenResult? {
    if (chatId.isEmpty() || msgId.isEmpty()) return null
    val c = ipc ?: run {
        appendStatus("open: ipc not connected", level = StatusLevel.WARN)
        return null
    }
    return try {
        val reply = c.request(
            type = FrameType.OpenFile,
            payload = OpenFileRequest(chatId, msgId).toJson(),
        )
        if (reply.type == FrameType.Error) {
            val err = reply.payload?.let(ErrorPayload::fromJson)
            appendStatus("open error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
            null
        } else {
            val resp = reply.payload?.let(OpenFileReadyResponse::fromJson) ?: return null
            OpenResult(
                path = resp.fullPath,
                sniffedMime = resp.sniffedMime,
                matches = resp.mimeMatches,
            )
        }
    } catch (t: Throwable) {
        appendStatus("open failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        null
    }
}


fun MessengerStore.recordImagePath(msgId: String, path: String) {
    if (msgId.isEmpty() || path.isEmpty()) return
    _imagePathByMsgId.update { it + (msgId to path) }
}


fun MessengerStore.recordImageDims(msgId: String, width: Int, height: Int) {
    if (msgId.isEmpty() || width <= 0 || height <= 0) return
    _imageDimsByMsgId.update { it + (msgId to (width to height)) }
}


internal fun MessengerStore.clearImageCaches() {
    _imagePathByMsgId.value = emptyMap()
    _imageDimsByMsgId.value = emptyMap()
}
