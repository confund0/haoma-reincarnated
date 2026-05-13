package io.haoma.calculator.messenger.status

import android.content.Context
import android.content.Intent
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.core.content.FileProvider
import io.haoma.calculator.log.Logger
import java.io.File
import java.io.FileOutputStream
import java.text.SimpleDateFormat
import java.util.Calendar
import java.util.Date
import java.util.Locale
import java.util.zip.ZipEntry
import java.util.zip.ZipOutputStream
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext


@Composable
fun LogsSection() {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()

    var components by remember { mutableStateOf(discoverComponents()) }
    var selected by remember { mutableStateOf(components.firstOrNull() ?: "haoma-gui") }
    var refreshKey by remember { mutableIntStateOf(0) }
    var lines by remember { mutableStateOf<List<String>>(emptyList()) }
    var loadError by remember { mutableStateOf<String?>(null) }
    var loading by remember { mutableStateOf(false) }
    var search by remember { mutableStateOf("") }
    var fromText by remember { mutableStateOf("") }
    var toText by remember { mutableStateOf("") }

    LaunchedEffect(selected, refreshKey) {
        loading = true
        loadError = null
        val result = runCatching {
            withContext(Dispatchers.IO) {
                val dir = Logger.logsDir() ?: return@withContext emptyList<String>()
                val f = File(dir, "$selected.log")
                if (!f.exists()) emptyList<String>() else f.readLines(Charsets.UTF_8)
            }
        }
        lines = result.getOrDefault(emptyList())
        loadError = result.exceptionOrNull()?.message
        loading = false
    }

    val timeWindow = remember(fromText, toText) { parseTimeWindow(fromText, toText) }
    val filtered = remember(lines, search, timeWindow) {
        applyFilters(lines, search, timeWindow)
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(BG_BASE)
            .padding(horizontal = 12.dp, vertical = 8.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        ActionRow(
            components = components,
            selected = selected,
            onSelect = { selected = it },
            onRefresh = {
                components = discoverComponents()
                refreshKey += 1
            },
            onExport = {
                scope.launch {
                    val msg = withContext(Dispatchers.IO) { exportLogs(context) }
                    
                    
                    Logger.i("logs", msg)
                }
            },
        )
        FilterRow(
            search = search,
            onSearchChange = { search = it },
            fromText = fromText,
            onFromChange = { fromText = it },
            toText = toText,
            onToChange = { toText = it },
            timeWindowValid = timeWindow.valid,
        )
        Box(modifier = Modifier.weight(1f).fillMaxWidth()) {
            when {
                loading -> CenterDim("loading…")
                loadError != null -> CenterDim("read failed: $loadError")
                lines.isEmpty() -> CenterDim("(no $selected.log yet)")
                filtered.isEmpty() -> CenterDim("(no matches)")
                else -> LogList(lines = filtered)
            }
        }
        Footer(total = lines.size, shown = filtered.size)
    }
}

@Composable
private fun ActionRow(
    components: List<String>,
    selected: String,
    onSelect: (String) -> Unit,
    onRefresh: () -> Unit,
    onExport: () -> Unit,
) {
    var dropdownOpen by remember { mutableStateOf(false) }
    Row(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(modifier = Modifier.weight(1f)) {
            Pill(label = selected, accent = true, onClick = { dropdownOpen = true })
            DropdownMenu(
                expanded = dropdownOpen,
                onDismissRequest = { dropdownOpen = false },
            ) {
                if (components.isEmpty()) {
                    DropdownMenuItem(
                        text = { Text("(no log files)", color = FG_DIM, fontFamily = FontFamily.Monospace) },
                        onClick = { dropdownOpen = false },
                    )
                } else {
                    components.forEach { name ->
                        DropdownMenuItem(
                            text = {
                                Text(
                                    text = name,
                                    color = if (name == selected) FG_LOG else FG_DIM,
                                    fontFamily = FontFamily.Monospace,
                                    fontSize = 13.sp,
                                )
                            },
                            onClick = {
                                onSelect(name)
                                dropdownOpen = false
                            },
                        )
                    }
                }
            }
        }
        Pill(label = "refresh", onClick = onRefresh)
        Pill(label = "export", onClick = onExport)
    }
}

@Composable
private fun FilterRow(
    search: String,
    onSearchChange: (String) -> Unit,
    fromText: String,
    onFromChange: (String) -> Unit,
    toText: String,
    onToChange: (String) -> Unit,
    timeWindowValid: Boolean,
) {
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        TinyField(
            value = search,
            onChange = onSearchChange,
            placeholder = "search…",
            keyboard = KeyboardType.Text,
            modifier = Modifier.fillMaxWidth(),
        )
        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(
                text = "from",
                color = FG_DIM,
                fontFamily = FontFamily.Monospace,
                fontSize = 12.sp,
            )
            TinyField(
                value = fromText,
                onChange = onFromChange,
                placeholder = "HH:MM",
                keyboard = KeyboardType.Number,
                modifier = Modifier.width(74.dp),
            )
            Text(
                text = "to",
                color = FG_DIM,
                fontFamily = FontFamily.Monospace,
                fontSize = 12.sp,
            )
            TinyField(
                value = toText,
                onChange = onToChange,
                placeholder = "HH:MM",
                keyboard = KeyboardType.Number,
                modifier = Modifier.width(74.dp),
            )
            if (!timeWindowValid) {
                Text(
                    text = "bad time",
                    color = C_WARN,
                    fontFamily = FontFamily.Monospace,
                    fontSize = 11.sp,
                )
            }
        }
    }
}

@Composable
private fun Pill(label: String, accent: Boolean = false, onClick: () -> Unit) {
    val border = if (accent) BORDER_CMD else BORDER_DIM
    Surface(
        modifier = Modifier
            .heightIn(min = 32.dp)
            .clickable(onClick = onClick),
        shape = RoundedCornerShape(percent = 50),
        color = BG_BAR,
        border = BorderStroke(1.dp, border),
    ) {
        Box(
            modifier = Modifier.padding(horizontal = 14.dp, vertical = 6.dp),
            contentAlignment = Alignment.Center,
        ) {
            Text(
                text = "[$label]",
                color = FG_LOG,
                fontFamily = FontFamily.Monospace,
                fontSize = 13.sp,
            )
        }
    }
}

@Composable
private fun TinyField(
    value: String,
    onChange: (String) -> Unit,
    placeholder: String,
    keyboard: KeyboardType,
    modifier: Modifier,
) {
    Surface(
        modifier = modifier.heightIn(min = 32.dp),
        shape = RoundedCornerShape(percent = 50),
        color = BG_BAR,
        border = BorderStroke(1.dp, BORDER_DIM),
    ) {
        BasicTextField(
            value = value,
            onValueChange = onChange,
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 14.dp, vertical = 8.dp),
            singleLine = true,
            textStyle = TextStyle(
                color = FG_LOG,
                fontFamily = FontFamily.Monospace,
                fontSize = 13.sp,
            ),
            cursorBrush = SolidColor(BORDER_CMD),
            keyboardOptions = KeyboardOptions(keyboardType = keyboard),
            decorationBox = { inner ->
                if (value.isEmpty()) {
                    Text(
                        text = placeholder,
                        color = FG_DIM,
                        fontFamily = FontFamily.Monospace,
                        fontSize = 13.sp,
                    )
                }
                inner()
            },
        )
    }
}

@Composable
private fun LogList(lines: List<String>) {
    val state = rememberLazyListState()
    LaunchedEffect(lines.size) {
        if (lines.isNotEmpty()) state.scrollToItem(lines.size - 1)
    }
    LazyColumn(
        state = state,
        modifier = Modifier.fillMaxSize(),
        contentPadding = PaddingValues(vertical = 4.dp),
    ) {
        items(lines) { line ->
            val color = when {
                line.contains("ERROR") || line.contains("FATAL") -> C_ERROR
                line.contains("WARN") -> C_WARN
                else -> FG_LOG
            }
            Text(
                text = line,
                color = color,
                fontFamily = FontFamily.Monospace,
                fontSize = 11.sp,
                modifier = Modifier.fillMaxWidth().padding(vertical = 1.dp),
            )
        }
    }
}

@Composable
private fun CenterDim(text: String) {
    Box(modifier = Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        Text(
            text = text,
            color = FG_DIM,
            fontFamily = FontFamily.Monospace,
            fontSize = 12.sp,
        )
    }
}

@Composable
private fun Footer(total: Int, shown: Int) {
    Text(
        text = if (total == shown) "$total lines" else "$shown / $total lines",
        color = FG_DIM,
        fontFamily = FontFamily.Monospace,
        fontSize = 11.sp,
    )
}


private fun discoverComponents(): List<String> {
    val dir = Logger.logsDir() ?: return emptyList()
    val names = dir.listFiles { f -> f.isFile && f.name.endsWith(".log") }
        ?.map { it.name.removeSuffix(".log") }
        ?: emptyList()
    
    
    return names.sortedWith(compareBy({ it != "haoma-gui" }, { it }))
}

private data class TimeWindow(val from: Long?, val to: Long?, val valid: Boolean) {
    fun contains(ts: Long): Boolean {
        if (from != null && ts < from) return false
        if (to != null && ts > to) return false
        return true
    }
}

private fun parseTimeWindow(from: String, to: String): TimeWindow {
    val fromMs = parseHHMM(from)
    val toMs = parseHHMM(to)
    val fromValid = from.isBlank() || fromMs != null
    val toValid = to.isBlank() || toMs != null
    return TimeWindow(from = fromMs, to = toMs, valid = fromValid && toValid)
}

private fun parseHHMM(text: String): Long? {
    if (text.isBlank()) return null
    val parts = text.split(":")
    if (parts.size != 2) return null
    val h = parts[0].toIntOrNull() ?: return null
    val m = parts[1].toIntOrNull() ?: return null
    if (h !in 0..23 || m !in 0..59) return null
    val cal = Calendar.getInstance()
    cal.set(Calendar.HOUR_OF_DAY, h)
    cal.set(Calendar.MINUTE, m)
    cal.set(Calendar.SECOND, 0)
    cal.set(Calendar.MILLISECOND, 0)
    return cal.timeInMillis
}

private fun applyFilters(
    lines: List<String>,
    search: String,
    window: TimeWindow,
): List<String> {
    val needle = search.trim().lowercase(Locale.US)
    val hasTimeFilter = window.from != null || window.to != null
    if (needle.isEmpty() && !hasTimeFilter) return lines
    return lines.filter { line ->
        val needleOk = needle.isEmpty() || line.lowercase(Locale.US).contains(needle)
        val timeOk = if (!hasTimeFilter) true else {
            val ts = parseLineTimestamp(line)
            ts == null || window.contains(ts)
        }
        needleOk && timeOk
    }
}


private fun parseLineTimestamp(line: String): Long? {
    val ts = when {
        line.startsWith("time=") && line.length >= 24 -> line.substring(5, 24)
        line.length >= 19 -> line.substring(0, 19)
        else -> return null
    }
    if (ts.length < 19) return null
    val date = ts.substring(0, 10)
    val time = ts.substring(11, 19)
    val timeParts = time.split(":")
    if (timeParts.size != 3) return null
    val h = timeParts[0].toIntOrNull() ?: return null
    val m = timeParts[1].toIntOrNull() ?: return null
    val s = timeParts[2].toIntOrNull() ?: return null
    if (h !in 0..23 || m !in 0..59 || s !in 0..59) return null
    val today = SimpleDateFormat("yyyy-MM-dd", Locale.US).format(Date())
    if (date != today) return null
    val cal = Calendar.getInstance()
    cal.set(Calendar.HOUR_OF_DAY, h)
    cal.set(Calendar.MINUTE, m)
    cal.set(Calendar.SECOND, s)
    cal.set(Calendar.MILLISECOND, 0)
    return cal.timeInMillis
}


private fun exportLogs(context: Context): String {
    val srcDir = Logger.logsDir() ?: return "export: logs dir missing"
    val files = srcDir.listFiles { f ->
        f.isFile && (f.name.endsWith(".log") || f.name.endsWith(".log.prev"))
    } ?: emptyArray()
    if (files.isEmpty()) return "export: no log files to zip"

    val outDir = File(context.cacheDir, "log-export").apply { mkdirs() }
    val stamp = SimpleDateFormat("yyyyMMdd-HHmmss", Locale.US).format(Date())
    val zip = File(outDir, "haoma-logs-$stamp.zip")

    ZipOutputStream(FileOutputStream(zip).buffered()).use { zos ->
        for (f in files) {
            zos.putNextEntry(ZipEntry(f.name))
            f.inputStream().use { it.copyTo(zos) }
            zos.closeEntry()
        }
    }

    val authority = context.packageName + ".fileprovider"
    val uri = FileProvider.getUriForFile(context, authority, zip)
    val send = Intent(Intent.ACTION_SEND).apply {
        type = "application/zip"
        putExtra(Intent.EXTRA_STREAM, uri)
        addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
    }
    val chooser = Intent.createChooser(send, "Share Haoma logs").apply {
        addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
    }
    context.startActivity(chooser)
    return "export: ${files.size} files → ${zip.name}"
}

private val BG_BASE = Color(0xFF1D2021)
private val BG_BAR = Color(0xFF282828)
private val FG_LOG = Color(0xFFD5C4A1)
private val FG_DIM = Color(0xFF7C6F64)
private val C_WARN = Color(0xFFFABD2F)
private val C_ERROR = Color(0xFFFB4934)
private val BORDER_CMD = Color(0xFF83A598)
private val BORDER_DIM = Color(0xFF3C3836)
