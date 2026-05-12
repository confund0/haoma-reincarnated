add_executable(haoma-mic
  main.cpp
  ${CMAKE_SOURCE_DIR}/${STREAMS_AUDIO_IMPL}
)
target_link_libraries(haoma-mic PRIVATE
  streams_common
  streams_opus
)
if(CMAKE_SYSTEM_NAME STREQUAL "Linux")
  target_link_libraries(haoma-mic PRIVATE PkgConfig::PIPEWIRE pthread)
endif()
# Android packages this PIE executable as lib*.so so PackageManager
# extracts it to nativeLibraryDir, where SELinux permits exec.
if(CMAKE_SYSTEM_NAME STREQUAL "Android")
  target_link_libraries(haoma-mic PRIVATE aaudio log)
  set_target_properties(haoma-mic PROPERTIES
    PREFIX "lib"
    SUFFIX ".so"
    OUTPUT_NAME "haoma-mic"
  )
endif()
