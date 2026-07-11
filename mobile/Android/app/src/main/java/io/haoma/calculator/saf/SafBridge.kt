package io.haoma.calculator.saf

import android.content.ContentResolver
import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.media.ExifInterface
import android.net.Uri
import io.haoma.calculator.core.ImageOrient
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
        val normalize = needsFrameworkNormalize(sourceMime)
        val finalName = if (normalize) renameToJpeg(originalName) else originalName
        val safe = sanitizeName(finalName)
        val dir = ensureDir(File(context.cacheDir, SAF_IN_DIR))
        val dest = File(dir, "${randomToken()}-$safe")
        val normalized = normalize && tryNormalizeToJpeg(resolver, uri, dest)
        if (!normalized) {
            
            
            resolver.openInputStream(uri).use { input ->
                requireNotNull(input) { "saf: openInputStream returned null for $uri" }
                FileOutputStream(dest).use { out -> input.copyTo(out) }
            }
        }
        Logger.d(
            "saf",
            "copy-in: $uri → ${dest.absolutePath} (${dest.length()} bytes, mime=$sourceMime, normalized=$normalized)",
        )
        return CopyInResult(path = dest.absolutePath, displayName = if (normalized) finalName else originalName)
    }

    
    private fun tryNormalizeToJpeg(resolver: ContentResolver, uri: Uri, dest: File): Boolean {
        var bitmap: Bitmap? = null
        return try {
            bitmap = resolver.openInputStream(uri).use { input ->
                requireNotNull(input) { "saf: openInputStream returned null for $uri" }
                BitmapFactory.decodeStream(input)
            } ?: return run {
                Logger.w("saf", "heic-normalize: decodeStream returned null for $uri; sending raw")
                false
            }
            
            
            val orientation = resolver.openInputStream(uri)?.use { ImageOrient.read(it) }
                ?: ExifInterface.ORIENTATION_NORMAL
            bitmap = ImageOrient.apply(bitmap, orientation)
            FileOutputStream(dest).use { out ->
                if (!bitmap.compress(Bitmap.CompressFormat.JPEG, JPEG_QUALITY, out)) {
                    Logger.w("saf", "heic-normalize: compress returned false for $uri; sending raw")
                    return false
                }
            }
            true
        } catch (t: OutOfMemoryError) {
            Logger.w("saf", "heic-normalize OOM for $uri; sending raw")
            dest.delete()
            false
        } catch (t: Throwable) {
            Logger.w("saf", "heic-normalize failed for $uri; sending raw: ${t.message ?: "?"}")
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

    
    fun shareIntent(context: Context, path: String, mime: String): Intent {
        val uri = fileProviderUri(context, path)
        return Intent(Intent.ACTION_SEND).apply {
            type = mime.ifEmpty { "*/*" }
            putExtra(Intent.EXTRA_STREAM, uri)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        }
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

    
    fun peekMetadata(context: Context, uri: Uri): UriMetadata {
        val resolver = context.contentResolver
        val name = queryDisplayName(resolver, uri) ?: "attachment"
        val mime = resolver.getType(uri).orEmpty()
        var size = 0L
        resolver.query(uri, arrayOf(OpenableColumns.SIZE), null, null, null)?.use { c ->
            if (c.moveToFirst()) {
                val idx = c.getColumnIndex(OpenableColumns.SIZE)
                if (idx >= 0 && !c.isNull(idx)) {
                    size = c.getLong(idx)
                }
            }
        }
        return UriMetadata(displayName = name, mime = mime, sizeBytes = size)
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

    
    private fun needsFrameworkNormalize(mime: String): Boolean {
        if (mime.isEmpty()) return false
        return when (mime.lowercase(Locale.US)) {
            "image/heic", "image/heif", "image/avif" -> true
            else -> false
        }
    }

    
    private fun renameToJpeg(originalName: String): String {
        val dot = originalName.lastIndexOf('.')
        val stem = if (dot > 0) originalName.substring(0, dot) else originalName
        return "$stem.jpg"
    }

    
    fun peekImageDims(context: Context, uri: Uri): ImageDims? {
        val resolver = context.contentResolver
        val opts = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        return try {
            resolver.openInputStream(uri).use { input ->
                if (input == null) return null
                BitmapFactory.decodeStream(input, null, opts)
            }
            val w = opts.outWidth
            val h = opts.outHeight
            if (w <= 0 || h <= 0) null else ImageDims(width = w, height = h)
        } catch (t: Throwable) {
            Logger.w("saf", "peekImageDims failed for $uri: ${t.message ?: "?"}")
            null
        }
    }

    private const val JPEG_QUALITY = 90
}

data class CopyInResult(val path: String, val displayName: String)


data class UriMetadata(val displayName: String, val mime: String, val sizeBytes: Long)


data class ImageDims(val width: Int, val height: Int)
