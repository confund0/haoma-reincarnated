#include "log.h"

namespace haoma::streams {
static const char* g_tag = "stream";
void set_log_tag(const char* tag) { g_tag = tag; }
const char* get_log_tag() { return g_tag; }
}
