add_executable(haoma-cam
  main.cpp
  ${CMAKE_SOURCE_DIR}/${STREAMS_VIDEO_IMPL}
)
target_link_libraries(haoma-cam PRIVATE
  streams_common
  streams_vpx
)
if(CMAKE_SYSTEM_NAME STREQUAL "Linux")
  target_link_libraries(haoma-cam PRIVATE pthread)
endif()
# Android packages this PIE executable as lib*.so so PackageManager
# extracts it to nativeLibraryDir, where SELinux permits exec.
if(CMAKE_SYSTEM_NAME STREQUAL "Android")
  target_link_libraries(haoma-cam PRIVATE camera2ndk mediandk log)
  set_target_properties(haoma-cam PROPERTIES
    PREFIX "lib"
    SUFFIX ".so"
    OUTPUT_NAME "haoma-cam"
  )
endif()
