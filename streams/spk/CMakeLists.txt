add_executable(haoma-spk
  main.cpp
  ${CMAKE_SOURCE_DIR}/${STREAMS_AUDIO_IMPL}
)
target_link_libraries(haoma-spk PRIVATE
  streams_common
  streams_opus
)
if(CMAKE_SYSTEM_NAME STREQUAL "Linux")
  target_link_libraries(haoma-spk PRIVATE PkgConfig::PIPEWIRE pthread)
endif()
# Android packages this PIE executable as lib*.so so PackageManager
# extracts it to nativeLibraryDir, where SELinux permits exec.
if(CMAKE_SYSTEM_NAME STREQUAL "Android")
  target_link_libraries(haoma-spk PRIVATE aaudio log)
  set_target_properties(haoma-spk PROPERTIES
    PREFIX "lib"
    SUFFIX ".so"
    OUTPUT_NAME "haoma-spk"
  )
endif()
