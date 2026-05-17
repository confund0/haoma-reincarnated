add_executable(haoma-vid main.cpp)
target_link_libraries(haoma-vid PRIVATE
  streams_common
  streams_vpx
)
if(CMAKE_SYSTEM_NAME STREQUAL "Linux")
  target_link_libraries(haoma-vid PRIVATE pthread)
endif()
# Android packages this PIE executable as lib*.so so PackageManager
# extracts it to nativeLibraryDir, where SELinux permits exec.
if(CMAKE_SYSTEM_NAME STREQUAL "Android")
  target_link_libraries(haoma-vid PRIVATE log)
  set_target_properties(haoma-vid PROPERTIES
    PREFIX "lib"
    SUFFIX ".so"
    OUTPUT_NAME "haoma-vid"
  )
endif()
