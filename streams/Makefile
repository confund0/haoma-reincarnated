BUILD_DIR := build
BIN_DIR   := ../tmp/bins

.PHONY: all configure build clean smoke smoke-bad test

all: build

configure:
	cmake -S . -B $(BUILD_DIR)

build: configure
	cmake --build $(BUILD_DIR) --parallel

clean:
	rm -rf $(BUILD_DIR)

smoke: build
	./smoke.sh

smoke-bad: build
	BAD_KEY=1 ./smoke.sh

test: build
	$(BIN_DIR)/streams_test
	$(BIN_DIR)/streams_control_test
	$(BIN_DIR)/streams_media_meter_test
	$(BIN_DIR)/streams_jitter_buffer_test
	$(BIN_DIR)/streams_playout_scaler_test
