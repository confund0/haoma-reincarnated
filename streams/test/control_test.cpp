// parse_command unit tests — the closed command vocabulary in
// streams/common/control.h.  Standalone binary; exits 0/1.

#include "../common/control.h"

#include <cstdio>

using namespace haoma::streams;

static int fails = 0;
#define EXPECT(cond, msg) do {                                                   \
  if (!(cond)) { std::fprintf(stderr, "FAIL: %s\n", msg); fails++; }             \
} while (0)

int main() {
  // Each well-formed command parses correctly.
  EXPECT(parse_command(R"({"cmd":"mute"})").cmd   == Command::Mute,   "mute");
  EXPECT(parse_command(R"({"cmd":"unmute"})").cmd == Command::Unmute, "unmute");
  EXPECT(parse_command(R"({"cmd":"stats"})").cmd  == Command::Stats,  "stats");
  EXPECT(parse_command(R"({"cmd":"exit"})").cmd   == Command::Exit,   "exit");

  {
    auto m = parse_command(R"({"cmd":"bitrate","kbps":24})");
    EXPECT(m.cmd == Command::Bitrate, "bitrate cmd");
    EXPECT(m.int_arg == 24,           "bitrate kbps=24");
  }

  // Whitespace-tolerant.
  EXPECT(parse_command(R"({  "cmd"  :  "mute"  })").cmd == Command::Mute,
         "whitespace-tolerant");

  // Reversed key order in bitrate (kbps before cmd).
  {
    auto m = parse_command(R"({"kbps":48,"cmd":"bitrate"})");
    EXPECT(m.cmd == Command::Bitrate, "reversed-order bitrate cmd");
    EXPECT(m.int_arg == 48,           "reversed-order bitrate kbps");
  }

  // Negative bitrate parses (validation lives in the streamer).
  {
    auto m = parse_command(R"({"cmd":"bitrate","kbps":-1})");
    EXPECT(m.cmd == Command::Bitrate, "negative kbps still parses");
    EXPECT(m.int_arg == -1,           "negative kbps value");
  }

  // Malformed / unknown / missing inputs return Command::Unknown.
  EXPECT(parse_command("").cmd                   == Command::Unknown, "empty line");
  EXPECT(parse_command("not json").cmd           == Command::Unknown, "not-json");
  EXPECT(parse_command(R"({"cmd":"bogus"})").cmd == Command::Unknown, "unknown verb");
  EXPECT(parse_command(R"({"cmd":"bitrate"})").cmd == Command::Unknown,
         "bitrate without kbps");
  EXPECT(parse_command(R"({"foo":"bar"})").cmd   == Command::Unknown, "no cmd field");
  EXPECT(parse_command(R"({"cmd":})").cmd        == Command::Unknown, "missing value");
  EXPECT(parse_command(R"({"cmd":"mute)").cmd    == Command::Unknown, "unterminated quote");

  if (fails) {
    std::fprintf(stderr, "%d failures\n", fails);
    return 1;
  }
  std::fprintf(stderr, "all passed\n");
  return 0;
}
