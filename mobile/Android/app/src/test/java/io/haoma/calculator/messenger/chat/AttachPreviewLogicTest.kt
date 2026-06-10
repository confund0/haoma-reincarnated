package io.haoma.calculator.messenger.chat

import io.haoma.calculator.saf.ImageDims
import io.haoma.calculator.saf.UriMetadata
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test


class AttachPreviewLogicTest {

    @Test
    fun estimateCompressedBytes_belowLongEdge_smallSaving() {
        val src = 1_000_000L
        val out = estimateCompressedBytes(src, ImageDims(1600, 1000))
        assertNotNull(out)
        
        assertEquals(950_000L, out)
    }

    @Test
    fun estimateCompressedBytes_oversize_shrinksWithPixelAreaRatio() {
        val src = 4_000_000L
        val out = estimateCompressedBytes(src, ImageDims(4000, 3000))
        assertNotNull(out!!)
        
        val expected = (4_000_000L * (1920.0 / 4000) * (1920.0 / 4000)).toLong()
        assertEquals(expected, out)
        assertTrue("must be a real shrink", out < src / 2)
    }

    @Test
    fun estimateCompressedBytes_portraitOversize_usesLongEdge() {
        val src = 4_000_000L
        val out = estimateCompressedBytes(src, ImageDims(1000, 4000))
        assertNotNull(out!!)
        
        val expected = (4_000_000L * (1920.0 / 4000) * (1920.0 / 4000)).toLong()
        assertEquals(expected, out)
    }

    @Test
    fun estimateCompressedBytes_missingDims_isNull() {
        assertNull(estimateCompressedBytes(2_000_000L, null))
    }

    @Test
    fun estimateCompressedBytes_zeroOrNegative_isNull() {
        assertNull(estimateCompressedBytes(0L, ImageDims(800, 600)))
        assertNull(estimateCompressedBytes(-1L, ImageDims(800, 600)))
    }

    @Test
    fun isCompressibleImage_acceptsDaemonAndNormalizedSet() {
        val mimes = listOf(
            "image/jpeg", "image/jpg", "image/png", "image/webp",
            "image/heic", "image/heif", "image/avif",
        )
        for (mime in mimes) {
            assertTrue("expected compressible: $mime",
                isCompressibleImage(UriMetadata("x", mime, 1L)))
        }
    }

    @Test
    fun isCompressibleImage_rejectsAnimatedAndNonImage() {
        val mimes = listOf(
            "image/gif", "image/svg+xml", "image/bmp",
            "video/mp4", "application/pdf", "text/plain", "",
        )
        for (mime in mimes) {
            assertFalse("expected NOT compressible: $mime",
                isCompressibleImage(UriMetadata("x", mime, 1L)))
        }
    }

    @Test
    fun subtitleFor_showsEstimateWithTilde_whenCompressed() {
        val meta = UriMetadata("photo.jpg", "image/jpeg", 4_000_000L)
        val out = subtitleFor(meta, compressed = true, estimatedBytes = 900_000L)
        assertTrue("expected '~' prefix on estimated size, got $out",
            out.startsWith("~"))
        assertTrue("expected mime in subtitle: $out", out.contains("image/jpeg"))
    }

    @Test
    fun subtitleFor_showsSourceSize_whenOriginal() {
        val meta = UriMetadata("photo.jpg", "image/jpeg", 4_000_000L)
        val out = subtitleFor(meta, compressed = false, estimatedBytes = null)
        assertFalse("no tilde prefix in original mode: $out", out.startsWith("~"))
        assertTrue("expected source size in subtitle: $out", out.contains("3.8 MB"))
    }

    @Test
    fun humanBytesShort_picksRightUnit() {
        assertEquals("", humanBytesShort(0L))
        assertEquals("500 B", humanBytesShort(500L))
        assertEquals("1.0 KB", humanBytesShort(1024L))
        assertEquals("1.0 MB", humanBytesShort(1024L * 1024))
        assertEquals("3.8 MB", humanBytesShort(4_000_000L))
    }
}
