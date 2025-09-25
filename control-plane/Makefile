XP_PKG_NAME ?= adhar-control-plane
XP_PKG_VERSION ?= 0.1.0
DIST_DIR ?= dist
XP_PKG ?= $(DIST_DIR)/$(XP_PKG_NAME)-$(XP_PKG_VERSION).xpkg
PACKAGE_SRC = configuration

.PHONY: default build lint package clean fmt

default: build

build: lint package

lint:
	@echo "› linting control-plane manifests and registry"
	@go test ./pkg/...

package:
	@echo "› building $(XP_PKG)"
	@rm -rf $(DIST_DIR)
	@mkdir -p $(DIST_DIR)
	@tar -czf $(XP_PKG) -C $(PACKAGE_SRC) .
	@echo "✓ package ready: $(XP_PKG)"

fmt:
	@echo "› formatting Go sources"
	@gofmt -w $$(find pkg -type f -name '*.go')

clean:
	@rm -rf $(DIST_DIR)
