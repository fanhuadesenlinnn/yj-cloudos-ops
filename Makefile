# yj-cloudos-ops 构建：支持 Linux / Windows / macOS × amd64 / arm64
# 用法:
#   make all            # 构建全部 6 个平台
#   make linux          # Linux amd64 + arm64
#   make windows        # Windows amd64 + arm64
#   make darwin         # macOS amd64 + arm64
#   make linux-amd64    # 单个平台
#   make clean

BIN_DIR := build
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# 目标平台: 平台/架构
TARGETS := \
	linux/amd64 \
	linux/arm64 \
	windows/amd64 \
	windows/arm64 \
	darwin/amd64 \
	darwin/arm64

.PHONY: all linux windows darwin clean tidy

all: $(TARGETS)

# 按平台分组的目标
linux: linux/amd64 linux/arm64
windows: windows/amd64 windows/arm64
darwin: darwin/amd64 darwin/arm64

# 通用构建规则: make <goos>/<goarch>
# 输出文件: build/yj-cloudos-ops-<goos>-<goarch>[.exe]
define BUILD_TARGET
$(1)/$(2):
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=$(1) GOARCH=$(2) go build -trimpath -ldflags="$(LDFLAGS)" \
		-o $(BIN_DIR)/yj-cloudos-ops-$(1)-$(2)$(if $(filter windows,$(1)),.exe,) .
	@echo "built: $(BIN_DIR)/yj-cloudos-ops-$(1)-$(2)$(if $(filter windows,$(1)),.exe,)"
endef

$(foreach t,$(TARGETS),$(eval $(call BUILD_TARGET,$(word 1,$(subst /, ,$(t))),$(word 2,$(subst /, ,$(t))))))

tidy:
	go mod tidy

clean:
	rm -rf $(BIN_DIR)
