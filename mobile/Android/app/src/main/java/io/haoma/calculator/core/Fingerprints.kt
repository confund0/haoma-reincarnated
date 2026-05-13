package io.haoma.calculator.core

import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import io.haoma.calculator.log.Logger
import java.io.File
import java.io.FileInputStream
import java.security.MessageDigest


data class FingerprintRow(
    val name: String,
    val path: String,
    val sha256: String,
)

data class BinaryFingerprints(
    val rows: List<FingerprintRow>,
)


fun computeFingerprints(context: Context): BinaryFingerprints {
    val libDir = File(context.applicationInfo.nativeLibraryDir)
    val rows = buildList {
        add(libRow(libDir, label = "haoma", file = "libhaoma.so"))
        add(libRow(libDir, label = "haomad", file = "libhaomad.so"))
        add(libRow(libDir, label = "haoma-mic", file = "libhaoma-mic.so"))
        add(libRow(libDir, label = "haoma-spk", file = "libhaoma-spk.so"))
        add(libRow(libDir, label = "libtor.so", file = "libtor.so"))
        add(apkRow(context))
        add(signerRow(context))
    }
    return BinaryFingerprints(rows = rows)
}

private fun libRow(libDir: File, label: String, file: String): FingerprintRow {
    val f = File(libDir, file)
    return FingerprintRow(
        name = label,
        path = f.absolutePath,
        sha256 = sha256OfFileOrEmpty(f),
    )
}

private fun apkRow(context: Context): FingerprintRow {
    val src = context.applicationInfo.sourceDir
    return FingerprintRow(
        name = "APK file",
        path = src,
        sha256 = sha256OfFileOrEmpty(File(src)),
    )
}

private fun signerRow(context: Context): FingerprintRow {
    val name = "APK signer"
    val packageName = context.packageName
    val pm = context.packageManager
    return try {
        @Suppress("DEPRECATION")
        val info = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            pm.getPackageInfo(packageName, PackageManager.GET_SIGNING_CERTIFICATES)
        } else {
            pm.getPackageInfo(packageName, PackageManager.GET_SIGNATURES)
        }
        val signerBytes: ByteArray? = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            info.signingInfo?.let { si ->
                val certs = if (si.hasMultipleSigners()) {
                    si.apkContentsSigners
                } else {
                    si.signingCertificateHistory
                }
                certs?.firstOrNull()?.toByteArray()
            }
        } else {
            @Suppress("DEPRECATION")
            info.signatures?.firstOrNull()?.toByteArray()
        }
        FingerprintRow(
            name = name,
            path = packageName,
            sha256 = signerBytes?.let { sha256OfBytes(it) }.orEmpty(),
        )
    } catch (t: Throwable) {
        Logger.w("fingerprints", "signer cert pull failed: ${t.message}")
        FingerprintRow(name = name, path = packageName, sha256 = "")
    }
}

private fun sha256OfFileOrEmpty(f: File): String {
    if (!f.exists()) return ""
    return try {
        val md = MessageDigest.getInstance("SHA-256")
        FileInputStream(f).use { stream ->
            val buf = ByteArray(64 * 1024)
            while (true) {
                val n = stream.read(buf)
                if (n <= 0) break
                md.update(buf, 0, n)
            }
        }
        toHex(md.digest())
    } catch (t: Throwable) {
        Logger.w("fingerprints", "sha256 of ${f.absolutePath} failed: ${t.message}")
        ""
    }
}

private fun sha256OfBytes(bytes: ByteArray): String {
    val md = MessageDigest.getInstance("SHA-256")
    md.update(bytes)
    return toHex(md.digest())
}

private fun toHex(bytes: ByteArray): String {
    val hex = CharArray(bytes.size * 2)
    val table = "0123456789abcdef".toCharArray()
    for (i in bytes.indices) {
        val b = bytes[i].toInt() and 0xff
        hex[i * 2] = table[b ushr 4]
        hex[i * 2 + 1] = table[b and 0x0f]
    }
    return String(hex)
}
