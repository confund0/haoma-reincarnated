add_executable(streams_test
  aead_test.cpp
)
target_link_libraries(streams_test PRIVATE streams_common)

add_executable(streams_control_test
  control_test.cpp
)
target_link_libraries(streams_control_test PRIVATE streams_common)

add_executable(streams_media_meter_test
  media_meter_test.cpp
)
target_link_libraries(streams_media_meter_test PRIVATE streams_common)

add_executable(streams_jitter_buffer_test
  jitter_buffer_test.cpp
)
target_link_libraries(streams_jitter_buffer_test PRIVATE streams_common)

add_executable(streams_playout_scaler_test
  playout_scaler_test.cpp
)
target_link_libraries(streams_playout_scaler_test PRIVATE streams_common)

enable_testing()
add_test(NAME streams_test                COMMAND streams_test)
add_test(NAME streams_control_test        COMMAND streams_control_test)
add_test(NAME streams_media_meter_test    COMMAND streams_media_meter_test)
add_test(NAME streams_jitter_buffer_test  COMMAND streams_jitter_buffer_test)
add_test(NAME streams_playout_scaler_test COMMAND streams_playout_scaler_test)
