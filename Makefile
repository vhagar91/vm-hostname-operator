# VM Hostname Operator Build Script
#
# This Makefile builds the VCF Service tarball using the Carvel toolchain.
# Prerequisites: ytt, kbld, imgpkg, kapp, docker (or podman)
#
# The build process:
#   1. Build the Go operator binary and container image
#   2. Push the image to a registry
#   3. Render all templates with build-time values
#   4. Lock image references using kbld
#   5. Package into a Carvel bundle
#   6. Export bundle to tarball for offline upload

NAME           := hostname-operator
VERSION        := 1.0.0
REGISTRY       ?= registry.example.com
IMAGE          ?= $(REGISTRY)/$(NAME):$(VERSION)
BUILD_DIR      := dist
BUNDLE_DIR     := .imgpkg
SUPERVISOR_DIR := supervisor-service

# Default target
.PHONY: all
all: clean build-go docker-build docker-push kbld-lock bundle tarball

# Clean build artifacts
.PHONY: clean
clean:
	rm -rf $(BUILD_DIR) $(SUPERVISOR_DIR)/bin
	rm -f $(BUNDLE_DIR)/images.yml

# Build the Go binary
.PHONY: build-go
build-go:
	cd $(SUPERVISOR_DIR) && CGO_ENABLED=0 go build -o bin/$(NAME) .

# Build the Docker image (using Dockerfile)
.PHONY: docker-build
docker-build: build-go
	@echo "Building Docker image: $(IMAGE)"
	@mkdir -p $(BUILD_DIR)
	cp $(SUPERVISOR_DIR)/bin/$(NAME) $(BUILD_DIR/
	
	# Create a temporary Dockerfile for the operator
	cat > $(BUILD_DIR)/Dockerfile << 'EOF'
	FROM gcr.io/distroless/static-debian12:nonroot
	COPY $(NAME) /usr/local/bin/$(NAME)
	USER 65532:65532
	ENTRYPOINT ["/usr/local/bin/$(NAME)"]
	EOF
	
	docker build -t $(IMAGE) -f $(BUILD_DIR)/Dockerfile $(BUILD_DIR)
	rm -f $(BUILD_DIR)/$(NAME) $(BUILD_DIR)/Dockerfile

# Push image to registry
.PHONY: docker-push
docker-push:
	docker push $(IMAGE)

# Lock image references for the VCF Service bundle
# Uses the build-time values to render and discover all image references
.PHONY: kbld-lock
kbld-lock:
	@echo "Locking image references..."
	@mkdir -p $(BUNDLE_DIR)
	kbld -f .values/render.yml \
		--imgpkg-lock-output $(BUNDLE_DIR)/images.yml \
		-l "img=$(IMAGE)"

# Build the Carvel bundle
.PHONY: bundle
bundle:
	@echo "Building Carvel bundle..."
	imgpkg push \
		--bundle $(REGISTRY)/$(NAME)-bundle:$(VERSION) \
		--file . \
		--image $(IMAGE)

# Export bundle to offline tarball
.PHONY: tarball
tarball:
	@echo "Exporting bundle to tarball..."
	@mkdir -p $(BUILD_DIR)
	imgpkg copy \
		--bundle $(REGISTRY)/$(NAME)-bundle:$(VERSION) \
		--to-tar $(BUILD_DIR)/$(NAME)-$(VERSION).tar

# Quick build: create the tarball directly (requires a local registry)
.PHONY: quick
quick:
	@echo "=== Quick Build ==="
	$(MAKE) docker-build
	$(MAKE) docker-push
	$(MAKE) kbld-lock
	$(MAKE) bundle
	$(MAKE) tarball
	@echo "=== Done ==="
	@echo "Tarball: $(BUILD_DIR)/$(NAME)-$(VERSION).tar"

# Alternative: Generate tarball without needing a full registry
# This creates a self-contained tarball that can be uploaded directly
.PHONY: offline-tarball
offline-tarball:
	@echo "Creating offline-ready tarball of the source bundle..."
	@mkdir -p $(BUILD_DIR)
	tar -cvf $(BUILD_DIR)/$(NAME)-$(VERSION).tar \
		--exclude='.git' \
		--exclude='dist' \
		--exclude='bin' \
		--exclude='.imgpkg' \
		.
	@echo "Tarball created: $(BUILD_DIR)/$(NAME)-$(VERSION).tar"
	@echo ""
	@echo "NOTE: This tarball contains source files only."
	@echo "For production, use 'make quick' to build with locked images."
	@echo "Then register the tarball via VCF Automation API:"
	@echo "  curl -X POST \$${API}/vcf-services \\"
	@echo "    -H 'Authorization: Bearer \$${TOKEN}' \\"
	@echo "    -H 'Content-Type: application/json' \\"
	@echo "    -d '{\"source\": \"https://depot.example.com:9443/$(NAME)-$(VERSION).tar\"}'"

# Validate the ytt templates render correctly
.PHONY: validate
validate:
	@echo "Validating ytt templates..."
	ytt -f config/ -f .values/render.yml --data-value "system.supervisorService.package=placeholder" --data-value "system.supervisorService.packageMetadata=placeholder" --output-files /tmp/hostname-operator-validate
	@echo "Templates validated successfully"
	@rm -rf /tmp/hostname-operator-validate

# Show help
.PHONY: help
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  all             Full build pipeline (default)"
	@echo "  build-go        Build the Go operator binary"
	@echo "  docker-build    Build Docker image"
	@echo "  docker-push     Push Docker image to registry"
	@echo "  kbld-lock       Lock image references"
	@echo "  bundle          Build Carvel bundle"
	@echo "  tarball         Export bundle to tarball"
	@echo "  quick           Run full pipeline"
	@echo "  offline-tarball Create source-only tarball"
	@echo "  validate        Validate ytt templates"
	@echo "  clean           Clean build artifacts"
	@echo "  help            Show this help"
	@echo ""
	@echo "Variables:"
	@echo "  REGISTRY=registry.example.com   Container registry (default)"
	@echo "  IMAGE=image:tag                 Full image reference"
	@echo "  VERSION=1.0.0                   Release version"