
#include "../common/control.h"

#include <cstdio>
#include <cstdlib>
#include <string>

using namespace haoma::streams;

static int fails = 0;
#define EXPECT(cond, msg) do {                                                   \
  if (!(cond)) { std::fprintf(stderr, "FAIL: %s\n", msg); fails++; }             \
} while (0)

int main() {
  EXPECT(parse_command(R"({"cmd":"mute"})").cmd   == Command::Mute,   "mute");
  EXPECT(parse_command(R"({"cmd":"unmute"})").cmd == Command::Unmute, "unmute");
  EXPECT(parse_command(R"({"cmd":"stats"})").cmd  == Command::Stats,  "stats");
  EXPECT(parse_command(R"({"cmd":"exit"})").cmd   == Command::Exit,   "exit");

  {
    auto m = parse_command(R"({"cmd":"bitrate","kbps":24})");
    EXPECT(m.cmd == Command::Bitrate, "bitrate cmd");
    EXPECT(m.int_arg == 24,           "bitrate kbps=24");
  }

  EXPECT(parse_command(R"({  "cmd"  :  "mute"  })").cmd == Command::Mute,
         "whitespace-tolerant");

  {
    auto m = parse_command(R"({"kbps":48,"cmd":"bitrate"})");
    EXPECT(m.cmd == Command::Bitrate, "reversed-order bitrate cmd");
    EXPECT(m.int_arg == 48,           "reversed-order bitrate kbps");
  }

  {
    auto m = parse_command(R"({"cmd":"bitrate","kbps":-1})");
    EXPECT(m.cmd == Command::Bitrate, "negative kbps still parses");
    EXPECT(m.int_arg == -1,           "negative kbps value");
  }

  EXPECT(parse_command("").cmd                   == Command::Unknown, "empty line");
  EXPECT(parse_command("not json").cmd           == Command::Unknown, "not-json");
  EXPECT(parse_command(R"({"cmd":"bogus"})").cmd == Command::Unknown, "unknown verb");
  EXPECT(parse_command(R"({"cmd":"bitrate"})").cmd == Command::Unknown,
         "bitrate without kbps");
  EXPECT(parse_command(R"({"foo":"bar"})").cmd   == Command::Unknown, "no cmd field");
  EXPECT(parse_command(R"({"cmd":})").cmd        == Command::Unknown, "missing value");
  EXPECT(parse_command(R"({"cmd":"mute)").cmd    == Command::Unknown, "unterminated quote");

  {
    const char* tmpdir = std::getenv("TMPDIR");
    std::string path = std::string(tmpdir ? tmpdir : "/tmp") + "/haoma_control_test_emit.log";
    FILE* f = std::freopen(path.c_str(), "w+", stdout);
    EXPECT(f != nullptr, "freopen stdout for emit capture");
    if (f) {
      emit_summary(R"("period_ms":1000,"frames_in":5)");
      emit_event("xrun", R"("xrun_count":3)");
      std::fflush(stdout);
      std::rewind(f);

      char line1[256] = {0};
      char line2[256] = {0};
      bool got1 = std::fgets(line1, sizeof(line1), f) != nullptr;
      bool got2 = std::fgets(line2, sizeof(line2), f) != nullptr;
      EXPECT(got1, "emit_summary wrote a line");
      EXPECT(got2, "emit_event wrote a line");
      std::string l1(line1), l2(line2);
      EXPECT(l1.rfind(R"({"type":"summary","period_ms":1000)", 0) == 0,
             "emit_summary line has the summary prefix");
      EXPECT(l2.rfind(R"({"type":"event","ev":"xrun","xrun_count":3)", 0) == 0,
             "emit_event line has the event prefix");
    }
    std::remove(path.c_str());
  }

  if (fails) {
    std::fprintf(stderr, "%d failures\n", fails);
    return 1;
  }
  std::fprintf(stderr, "all passed\n");
  return 0;
}
