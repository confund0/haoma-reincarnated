add_executable(haoma-mic
  main.cpp
  ${CMAKE_SOURCE_DIR}/${STREAMS_AUDIO_IMPL}
)
target_link_libraries(haoma-mic PRIVATE
  streams_common
  PkgConfig::OPUS
)
if(CMAKE_SYSTEM_NAME STREQUAL "Linux")
  target_link_libraries(haoma-mic PRIVATE PkgConfig::PIPEWIRE pthread)
endif()
