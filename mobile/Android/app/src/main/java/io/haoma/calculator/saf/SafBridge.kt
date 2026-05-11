package io.haoma.calculator.saf

import android.content.ContentResolver
import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.net.Uri
import android.provider.OpenableColumns
import androidx.core.content.FileProvider
import io.haoma.calculator.log.Logger
import java.io.File
import java.io.FileOutputStream
import java.security.SecureRandom
import java.util.Locale


object SafBridge {

    private const val PROVIDER_AUTHORITY_SUFFIX = ".fileprovider"
    private const val SAF_IN_DIR = "saf-in"
    private const val SAF_OUT_DIR = "saf-out"

    
    fun copyUriToCache(context: Context, uri: Uri): CopyInResult {
        val resolver = context.contentResolver
        val originalName = queryDisplayName(resolver, uri) ?: "attachment"
        val sourceMime = resolver.getType(uri).orEmpty()
        val format = stripFormatFor(sourceMime)
        val finalName = if (format != null) renameForFormat(originalName, format) else originalName
        val safe = sanitizeName(finalName)
        val dir = ensureDir(File(context.cacheDir, SAF_IN_DIR))
        val dest = File(dir, "${randomToken()}-$safe")
        val stripped = format != null && tryStripImage(resolver, uri, format, dest)
        if (!stripped) {
            
            
            resolver.openInputStream(uri).use { input ->
                requireNotNull(input) { "saf: openInputStream returned null for $uri" }
                FileOutputStream(dest).use { out -> input.copyTo(out) }
            }
        }
        Logger.d(
            "saf",
            "copy-in: $uri → ${dest.absolutePath} (${dest.length()} bytes, mime=$sourceMime, stripped=$stripped)",
        )
        return CopyInResult(path = dest.absolutePath, displayName = if (stripped) finalName else originalName)
    }

    
    private fun tryStripImage(
        resolver: ContentResolver,
        uri: Uri,
        format: Bitmap.CompressFormat,
        dest: File,
    ): Boolean {
        var bitmap: Bitmap? = null
        return try {
            bitmap = resolver.openInputStream(uri).use { input ->
                requireNotNull(input) { "saf: openInputStream returned null for $uri" }
                BitmapFactory.decodeStream(input)
            } ?: return run {
                Logger.w("saf", "exif-strip: decodeStream returned null for $uri; sending raw")
                false
            }
            val quality = if (format == Bitmap.CompressFormat.PNG) 100 else JPEG_QUALITY
            FileOutputStream(dest).use { out ->
                if (!bitmap.compress(format, quality, out)) {
                    Logger.w("saf", "exif-strip: compress returned false for $uri; sending raw")
                    return false
                }
            }
            true
        } catch (t: OutOfMemoryError) {
            Logger.w("saf", "exif-strip OOM for $uri; sending raw")
            
            
            dest.delete()
            false
        } catch (t: Throwable) {
            Logger.w("saf", "exif-strip failed for $uri; sending raw: ${t.message ?: "?"}")
            dest.delete()
            false
        } finally {
            bitmap?.recycle()
        }
    }

    
    fun copyDaemonOutputToUri(
        context: Context,
        sourcePath: String,
        destUri: Uri,
    ): Long {
        val src = File(sourcePath)
        require(src.exists()) { "saf: daemon output missing: $sourcePath" }
        val resolver = context.contentResolver
        var copied = 0L
        src.inputStream().use { input ->
            resolver.openOutputStream(destUri).use { out ->
                requireNotNull(out) { "saf: openOutputStream returned null for $destUri" }
                copied = input.copyTo(out)
            }
        }
        if (!src.delete()) {
            Logger.w("saf", "copy-out: temp survived deletion: $sourcePath")
        }
        Logger.d("saf", "copy-out: $sourcePath → $destUri ($copied bytes)")
        return copied
    }

    
    fun saveOutDir(context: Context): File =
        ensureDir(File(context.filesDir, SAF_OUT_DIR))

    
    fun viewIntent(context: Context, path: String, mime: String): Intent {
        val authority = context.packageName + PROVIDER_AUTHORITY_SUFFIX
        val uri = FileProvider.getUriForFile(context, authority, File(path))
        return Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, mime.ifEmpty { "*/*" })
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        }
    }

    
    fun fileProviderUri(context: Context, path: String): Uri {
        val authority = context.packageName + PROVIDER_AUTHORITY_SUFFIX
        return FileProvider.getUriForFile(context, authority, File(path))
    }

    fun deleteCacheCopy(path: String) {
        val f = File(path)
        if (!f.exists()) return
        if (!f.delete()) Logger.w("saf", "cache-copy survived deletion: $path")
    }

    private fun ensureDir(dir: File): File {
        if (!dir.exists() && !dir.mkdirs()) {
            error("saf: failed to create $dir")
        }
        return dir
    }

    private fun queryDisplayName(resolver: ContentResolver, uri: Uri): String? {
        resolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { c ->
            if (c.moveToFirst()) {
                val idx = c.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                if (idx >= 0) return c.getString(idx)
            }
        }
        return uri.lastPathSegment
    }

    
    private fun sanitizeName(raw: String): String {
        val cleaned = raw.replace(Regex("""[/\\\u0000]"""), "_").trim()
        if (cleaned.isEmpty()) return "attachment"
        return cleaned.take(80)
    }

    private val rng = SecureRandom()
    private fun randomToken(): String {
        val bytes = ByteArray(6)
        rng.nextBytes(bytes)
        return bytes.joinToString("") { String.format(Locale.US, "%02x", it) }
    }

    
    private fun stripFormatFor(mime: String): Bitmap.CompressFormat? {
        if (mime.isEmpty()) return null
        val m = mime.lowercase(Locale.US)
        if (!m.startsWith("image/")) return null
        return when (m) {
            "image/jpeg", "image/jpg", "image/heic", "image/heif", "image/webp", "image/avif" ->
                Bitmap.CompressFormat.JPEG
            "image/png" -> Bitmap.CompressFormat.PNG
            else -> null
        }
    }

    
    private fun renameForFormat(originalName: String, format: Bitmap.CompressFormat): String {
        val ext = when (format) {
            Bitmap.CompressFormat.PNG -> "png"
            else -> "jpg"
        }
        val dot = originalName.lastIndexOf('.')
        val stem = if (dot > 0) originalName.substring(0, dot) else originalName
        return "$stem.$ext"
    }

    private const val JPEG_QUALITY = 90
}

data class CopyInResult(val path: String, val displayName: String)
