cmake_minimum_required(VERSION 3.16)
project(haoma_streams CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

# Output goes to tmp/bins on host builds, into the APK's jniLibs/<abi>/
# tree on Android cross-builds. Android's PackageManager only extracts
# files matching lib*.so into nativeLibraryDir, where exec is allowed
# (W^X is enforced everywhere else from API 29). The mic/spk targets
# rename their output accordingly via PREFIX/SUFFIX below.
if(CMAKE_SYSTEM_NAME STREQUAL "Android")
  set(CMAKE_RUNTIME_OUTPUT_DIRECTORY
      "${CMAKE_SOURCE_DIR}/../mobile/Android/app/src/main/jniLibs/${ANDROID_ABI}")
else()
  set(CMAKE_RUNTIME_OUTPUT_DIRECTORY "${CMAKE_SOURCE_DIR}/../tmp/bins")
endif()

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

# Same shape for video capture (V-1.5: cam owns its sensor). Linux uses
# a Y4M file-source dev backend; Android uses NdkCamera. Real V4L2
# capture lands in V-4 (post-beta).
if(CMAKE_SYSTEM_NAME STREQUAL "Linux")
  set(STREAMS_VIDEO_IMPL "platform/video_y4m.cpp")
elseif(CMAKE_SYSTEM_NAME STREQUAL "Android")
  set(STREAMS_VIDEO_IMPL "platform/video_android.cpp")
elseif(CMAKE_SYSTEM_NAME STREQUAL "Darwin" OR CMAKE_SYSTEM_NAME STREQUAL "Windows")
  set(STREAMS_VIDEO_IMPL "platform/video_y4m.cpp")  # Y4M stub until V-4 lands real backends
else()
  message(FATAL_ERROR "streams: unsupported platform ${CMAKE_SYSTEM_NAME}")
endif()
message(STATUS "streams: video backend = ${STREAMS_VIDEO_IMPL}")

# Opus + libsodium + libvpx come from different sources per platform:
#   - Host builds (Linux/Darwin/Windows host dev): system pkg-config.
#   - Android cross-build: vendored static archives from
#     mobile/streams-build/prebuilt/<abi>/ (produced by `make streams-deps`).
# All three are wrapped in INTERFACE targets so mic/spk/cam/vid/common
# link the same name.
add_library(streams_opus   INTERFACE)
add_library(streams_sodium INTERFACE)
add_library(streams_vpx    INTERFACE)

if(CMAKE_SYSTEM_NAME STREQUAL "Android")
  set(STREAMS_PREBUILT "${CMAKE_SOURCE_DIR}/../mobile/streams-build/prebuilt/${ANDROID_ABI}")
  if(NOT EXISTS "${STREAMS_PREBUILT}/lib/libopus.a")
    message(FATAL_ERROR "streams: missing ${STREAMS_PREBUILT}/lib/libopus.a — run 'make streams-deps'")
  endif()
  if(NOT EXISTS "${STREAMS_PREBUILT}/lib/libsodium.a")
    message(FATAL_ERROR "streams: missing ${STREAMS_PREBUILT}/lib/libsodium.a — run 'make streams-deps'")
  endif()
  if(NOT EXISTS "${STREAMS_PREBUILT}/lib/libvpx.a")
    message(FATAL_ERROR "streams: missing ${STREAMS_PREBUILT}/lib/libvpx.a — run 'make streams-deps'")
  endif()
  target_include_directories(streams_opus   INTERFACE "${STREAMS_PREBUILT}/include")
  target_link_libraries     (streams_opus   INTERFACE "${STREAMS_PREBUILT}/lib/libopus.a")
  target_include_directories(streams_sodium INTERFACE "${STREAMS_PREBUILT}/include")
  target_link_libraries     (streams_sodium INTERFACE "${STREAMS_PREBUILT}/lib/libsodium.a")
  target_include_directories(streams_vpx    INTERFACE "${STREAMS_PREBUILT}/include")
  target_link_libraries     (streams_vpx    INTERFACE "${STREAMS_PREBUILT}/lib/libvpx.a")
else()
  find_package(PkgConfig REQUIRED)
  pkg_check_modules(OPUS   REQUIRED IMPORTED_TARGET opus)
  pkg_check_modules(SODIUM REQUIRED IMPORTED_TARGET libsodium)
  pkg_check_modules(VPX    REQUIRED IMPORTED_TARGET vpx)
  target_link_libraries(streams_opus   INTERFACE PkgConfig::OPUS)
  target_link_libraries(streams_sodium INTERFACE PkgConfig::SODIUM)
  target_link_libraries(streams_vpx    INTERFACE PkgConfig::VPX)
  if(CMAKE_SYSTEM_NAME STREQUAL "Linux")
    pkg_check_modules(PIPEWIRE REQUIRED IMPORTED_TARGET libpipewire-0.3)
  endif()
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
target_link_libraries(streams_common PUBLIC streams_sodium)

add_subdirectory(mic)
add_subdirectory(spk)
add_subdirectory(cam)
add_subdirectory(vid)
# Tests are host-only — they can't execute on a cross-built target.
if(NOT CMAKE_SYSTEM_NAME STREQUAL "Android")
  add_subdirectory(test)
endif()
