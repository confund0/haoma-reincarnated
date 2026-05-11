add_executable(streams_test
  aead_test.cpp
)
target_link_libraries(streams_test PRIVATE streams_common)

add_executable(streams_control_test
  control_test.cpp
)
target_link_libraries(streams_control_test PRIVATE streams_common)

enable_testing()
add_test(NAME streams_test         COMMAND streams_test)
add_test(NAME streams_control_test COMMAND streams_control_test)
