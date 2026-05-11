cmake_minimum_required(VERSION 3.16)
project(haoma_streams CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)
set(CMAKE_RUNTIME_OUTPUT_DIRECTORY "${CMAKE_SOURCE_DIR}/../tmp/bins")

if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Debug)
endif()

add_compile_options(-Wall -Wextra)
# Note: -Wpedantic is intentionally off — our LOG_*() macros use the GNU
# `, ##__VA_ARGS__` extension (no portable C++17 alternative; __VA_OPT__ is C++20).

# Exactly one platform audio impl is compiled per build (ADR-040 multiplatform shape).
# Calls-1c only fully wires Linux/PipeWire; the other branches are stubs.
if(CMAKE_SYSTEM_NAME STREQUAL "Linux")
  set(STREAMS_AUDIO_IMPL "platform/audio_pipewire.cpp")
elseif(CMAKE_SYSTEM_NAME STREQUAL "Android")
  set(STREAMS_AUDIO_IMPL "platform/audio_android.cpp")
elseif(CMAKE_SYSTEM_NAME STREQUAL "Windows")
  set(STREAMS_AUDIO_IMPL "platform/audio_wasapi.cpp")
elseif(CMAKE_SYSTEM_NAME STREQUAL "Darwin")
  set(STREAMS_AUDIO_IMPL "platform/audio_coreaudio.cpp")
else()
  message(FATAL_ERROR "streams: unsupported platform ${CMAKE_SYSTEM_NAME}")
endif()
message(STATUS "streams: audio backend = ${STREAMS_AUDIO_IMPL}")

find_package(PkgConfig REQUIRED)
pkg_check_modules(OPUS REQUIRED IMPORTED_TARGET opus)
pkg_check_modules(SODIUM REQUIRED IMPORTED_TARGET libsodium)
if(CMAKE_SYSTEM_NAME STREQUAL "Linux")
  pkg_check_modules(PIPEWIRE REQUIRED IMPORTED_TARGET libpipewire-0.3)
endif()

add_library(streams_common STATIC
  common/framing.cpp
  common/socket.cpp
  common/log.cpp
  common/aead.cpp
  common/key_fd.cpp
  common/control.cpp
)
target_include_directories(streams_common PUBLIC ${CMAKE_SOURCE_DIR})
target_link_libraries(streams_common PUBLIC PkgConfig::SODIUM)

add_subdirectory(mic)
add_subdirectory(spk)
add_subdirectory(test)
