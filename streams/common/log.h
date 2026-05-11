#pragma once
#include <cstdio>

namespace haoma::streams {
void set_log_tag(const char* tag);
const char* get_log_tag();
}

#define LOG_INFO(fmt, ...) ::fprintf(stderr, "[%s] INFO: " fmt "\n", ::haoma::streams::get_log_tag(), ##__VA_ARGS__)
#define LOG_ERR(fmt,  ...) ::fprintf(stderr, "[%s] ERR : " fmt "\n", ::haoma::streams::get_log_tag(), ##__VA_ARGS__)
#define LOG_DBG(fmt,  ...) ::fprintf(stderr, "[%s] DBG : " fmt "\n", ::haoma::streams::get_log_tag(), ##__VA_ARGS__)
