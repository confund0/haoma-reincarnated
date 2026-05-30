# Android: cam is encoder-only — capture lives in the Kotlin
# CameraSource (app process) and frames arrive over cam's AF_UNIX raw
# listener via --input-from-raw. The NDK Camera2 backend
# (platform/video_android.cpp) is intentionally not linked here; a noop
# make_video_capture() satisfies the symbol. video_android.cpp stays in
# the tree one slice for diff readability; post-Slice 5 cleanup deletes.
if(CMAKE_SYSTEM_NAME STREQUAL "Android")
  set(_cam_video_impl ${CMAKE_SOURCE_DIR}/platform/video_noop.cpp)
else()
  set(_cam_video_impl ${CMAKE_SOURCE_DIR}/${STREAMS_VIDEO_IMPL})
endif()

add_executable(haoma-cam
  main.cpp
  ${_cam_video_impl}
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
  target_link_libraries(haoma-cam PRIVATE log)
  set_target_properties(haoma-cam PROPERTIES
    PREFIX "lib"
    SUFFIX ".so"
    OUTPUT_NAME "haoma-cam"
  )
endif()
