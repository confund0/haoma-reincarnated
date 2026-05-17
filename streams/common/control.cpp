#include "control.h"
#include "log.h"

#include <cctype>
#include <cerrno>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <mutex>
#include <thread>
#include <poll.h>
#include <unistd.h>

namespace haoma::streams {

namespace {

std::mutex g_emit_mu;

// Find the value of "key": "..." somewhere in s.  Doesn't handle JSON
// escapes — the wire vocabulary has none.
struct StringField { std::string value; bool found = false; };
StringField extract_string_field(const std::string& s, const std::string& key) {
  std::string needle = "\"" + key + "\"";
  size_t k = s.find(needle);
  if (k == std::string::npos) return {};
  size_t pos = k + needle.size();
  while (pos < s.size() && std::isspace((unsigned char)s[pos])) pos++;
  if (pos >= s.size() || s[pos] != ':') return {};
  pos++;
  while (pos < s.size() && std::isspace((unsigned char)s[pos])) pos++;
  if (pos >= s.size() || s[pos] != '"') return {};
  size_t open  = pos + 1;
  size_t close = s.find('"', open);
  if (close == std::string::npos) return {};
  return {s.substr(open, close - open), true};
}

// Find "key": <integer>.  Returns {value, true} on success.
struct IntField { long long value = 0; bool found = false; };
IntField extract_int_field(const std::string& s, const std::string& key) {
  std::string needle = "\"" + key + "\"";
  size_t k = s.find(needle);
  if (k == std::string::npos) return {};
  size_t after = k + needle.size();
  while (after < s.size() && std::isspace((unsigned char)s[after])) after++;
  if (after >= s.size() || s[after] != ':') return {};
  after++;
  while (after < s.size() && std::isspace((unsigned char)s[after])) after++;
  if (after >= s.size()) return {};
  // Optional sign
  size_t end = after;
  if (s[end] == '-' || s[end] == '+') end++;
  size_t digits_start = end;
  while (end < s.size() && std::isdigit((unsigned char)s[end])) end++;
  if (end == digits_start) return {};
  try {
    long long v = std::stoll(s.substr(after, end - after));
    return {v, true};
  } catch (...) {
    return {};
  }
}

}  // namespace

ControlMsg parse_command(const std::string& line) {
  ControlMsg msg;
  auto cmd = extract_string_field(line, "cmd");
  if (!cmd.found) return msg;

  if      (cmd.value == "mute")    msg.cmd = Command::Mute;
  else if (cmd.value == "unmute")  msg.cmd = Command::Unmute;
  else if (cmd.value == "stats")   msg.cmd = Command::Stats;
  else if (cmd.value == "exit")    msg.cmd = Command::Exit;
  else if (cmd.value == "bitrate") {
    auto kbps = extract_int_field(line, "kbps");
    if (!kbps.found) return msg;  // missing arg → Unknown
    msg.cmd     = Command::Bitrate;
    msg.int_arg = (int)kbps.value;
  }
  return msg;
}

namespace {

void emit_line(const char* json) {
  std::lock_guard<std::mutex> lk(g_emit_mu);
  std::fputs(json, stdout);
  std::fputc('\n', stdout);
  std::fflush(stdout);
}

// Tiny JSON-string escaper for free-form `reason` fields.  Backslash
// and quote get escaped; everything else passes through as-is.  No
// control-char handling — reasons are short ASCII strings minted by us.
std::string esc(const std::string& in) {
  std::string out;
  out.reserve(in.size() + 4);
  for (char c : in) {
    if (c == '"' || c == '\\') out.push_back('\\');
    out.push_back(c);
  }
  return out;
}

}  // namespace

void emit_ready() {
  emit_line("{\"type\":\"ready\"}");
}

void emit_ready(const std::string& raw_unix) {
  std::string s = "{\"type\":\"ready\",\"raw_unix\":\"" + esc(raw_unix) + "\"}";
  emit_line(s.c_str());
}

void emit_stats(const Stats& s, double cpu_pct) {
  char buf[256];
  std::snprintf(buf, sizeof(buf),
    "{\"type\":\"stats\","
    "\"bytes_in\":%llu,\"bytes_out\":%llu,"
    "\"frames_in\":%llu,\"frames_out\":%llu,"
    "\"frames_dropped\":%llu,"
    "\"jitter_ms\":%.2f,\"cpu_pct\":%.1f}",
    (unsigned long long)s.bytes_in.load(),
    (unsigned long long)s.bytes_out.load(),
    (unsigned long long)s.frames_in.load(),
    (unsigned long long)s.frames_out.load(),
    (unsigned long long)s.frames_dropped.load(),
    (double)s.jitter_ms_x100.load() / 100.0,
    cpu_pct);
  emit_line(buf);
}

void emit_warn(const std::string& reason) {
  std::string s = "{\"type\":\"warn\",\"reason\":\"" + esc(reason) + "\"}";
  emit_line(s.c_str());
}

void emit_error(const std::string& reason) {
  std::string s = "{\"type\":\"error\",\"reason\":\"" + esc(reason) + "\"}";
  emit_line(s.c_str());
}

void emit_trace_frame(uint64_t counter, uint32_t bytes, bool muted) {
  char buf[128];
  std::snprintf(buf, sizeof(buf),
    "{\"type\":\"trace\",\"counter\":%llu,\"bytes\":%u,\"muted\":%s}",
    (unsigned long long)counter, bytes, muted ? "true" : "false");
  emit_line(buf);
}

void emit_clock_sample(int64_t local_ns, int64_t sender_pts_ns) {
  char buf[128];
  std::snprintf(buf, sizeof(buf),
    "{\"type\":\"clock_sample\",\"local_ns\":%lld,\"sender_pts_ns\":%lld}",
    (long long)local_ns, (long long)sender_pts_ns);
  emit_line(buf);
}

double CpuSampler::sample() {
  // /proc/self/stat: pid (comm) state ppid pgrp ... utime(14) stime(15) ...
  // comm can contain spaces and parentheses, so parse from the LAST ')'.
  std::FILE* f = std::fopen("/proc/self/stat", "r");
  if (!f) return 0.0;
  char buf[1024];
  if (!std::fgets(buf, sizeof(buf), f)) { std::fclose(f); return 0.0; }
  std::fclose(f);

  char* rp = std::strrchr(buf, ')');
  if (!rp) return 0.0;
  unsigned long utime = 0, stime = 0;
  // After the ')', fields are 0-indexed offset.  utime=12, stime=13.
  int n = std::sscanf(rp + 1, " %*c %*d %*d %*d %*d %*d %*u %*u %*u %*u %*u %lu %lu",
                      &utime, &stime);
  if (n != 2) return 0.0;

  long ticks_per_sec = ::sysconf(_SC_CLK_TCK);
  if (ticks_per_sec <= 0) return 0.0;

  auto     now    = std::chrono::steady_clock::now();
  uint64_t ticks  = (uint64_t)utime + (uint64_t)stime;
  double   pct    = 0.0;
  if (primed_) {
    double dt = std::chrono::duration<double>(now - last_time_).count();
    if (dt > 0.0) {
      double cpu_sec = (double)(ticks - last_ticks_) / (double)ticks_per_sec;
      pct = 100.0 * cpu_sec / dt;
    }
  }
  last_ticks_ = ticks;
  last_time_  = now;
  primed_     = true;
  return pct;
}

void JitterTracker::on_frame_arrival(std::chrono::steady_clock::time_point now) {
  if (!primed_) {
    last_   = now;
    primed_ = true;
    return;
  }
  // Sender pacing = 20 ms per frame.  |actual - 20ms| feeds RFC3550 EWMA.
  double iat_ms   = std::chrono::duration<double, std::milli>(now - last_).count();
  double dev_ms   = iat_ms - 20.0;
  if (dev_ms < 0) dev_ms = -dev_ms;
  j_ms_  += (dev_ms - j_ms_) / 16.0;
  last_   = now;
}

void control_loop(int fd,
                  std::atomic<bool>& done,
                  std::function<void(const ControlMsg&)> on_msg) {
  std::string line;
  line.reserve(256);
  pollfd p{};
  p.fd     = fd;
  p.events = POLLIN;

  while (!done.load()) {
    p.revents = 0;
    int r = ::poll(&p, 1, 100);
    if (r < 0) {
      if (errno == EINTR) continue;
      return;
    }
    if (r == 0) continue;
    if (p.revents & (POLLERR | POLLNVAL)) return;
    if (!(p.revents & (POLLIN | POLLHUP))) continue;

    char buf[256];
    ssize_t n = ::read(fd, buf, sizeof(buf));
    if (n < 0) {
      if (errno == EINTR) continue;
      return;
    }
    if (n == 0) return;  // EOF — quietly stop reading; streamer keeps running.

    for (ssize_t i = 0; i < n; ++i) {
      char c = buf[i];
      if (c == '\n') {
        if (!line.empty()) {
          on_msg(parse_command(line));
          line.clear();
        }
      } else if (c != '\r') {
        if (line.size() < 1024) line.push_back(c);
        else line.clear();  // pathological: drop oversized line
      }
    }
  }
}

void stats_loop(Stats& s,
                JitterTracker* jt,
                std::atomic<bool>& done,
                std::atomic<bool>& trace,
                std::atomic<bool>& request_now) {
  CpuSampler cpu;
  cpu.sample();  // prime — first real value comes next iteration
  while (!done.load()) {
    int target_ms = trace.load() ? 200 : 2000;
    for (int slept = 0; slept < target_ms && !done.load(); slept += 100) {
      if (request_now.load()) break;
      std::this_thread::sleep_for(std::chrono::milliseconds(100));
    }
    if (done.load()) break;
    request_now.store(false);
    if (jt) jt->store_into(s);
    emit_stats(s, cpu.sample());
  }
}

void JitterTracker::store_into(Stats& s) const {
  double v = j_ms_ * 100.0;  // 0.01 ms units
  if (v < 0) v = 0;
  if (v > (double)UINT32_MAX) v = (double)UINT32_MAX;
  s.jitter_ms_x100.store((uint32_t)v);
}

}  // namespace haoma::streams
