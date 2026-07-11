package io.haoma.calculator.messenger

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Test


class CaptionPayloadsTest {
    @Test fun sendFileRequestEncodesCaption() {
        val req = SendFileRequest("peer-1", "/tmp/x.jpg", "compressed", "holiday pic").toJson()
        assertEquals("holiday pic", req.getString("caption"))
    }

    @Test fun sendFileRequestOmitsEmptyCaption() {
        val req = SendFileRequest("peer-1", "/tmp/x.jpg", "compressed").toJson()
        assertFalse(req.has("caption"))
    }

    @Test fun fileEventBodyParsesCaption() {
        val o = JSONObject(
            """{"display_name":"x.jpg","size":42,"mime":"image/jpeg","state":"ready","caption":"holiday pic"}""",
        )
        assertEquals("holiday pic", FileEventBody.fromJson(o).caption)
    }

    @Test fun fileEventBodyDefaultsCaptionEmpty() {
        val o = JSONObject(
            """{"display_name":"x.jpg","size":42,"mime":"image/jpeg","state":"ready"}""",
        )
        assertEquals("", FileEventBody.fromJson(o).caption)
    }
}
