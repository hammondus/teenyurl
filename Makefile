BINARY := teenyurl
DIST   := dist

# The deploy target is a linux/arm64 box behind Nginx Proxy Manager, never the
# dev machine's architecture.
RELEASE_OS   := linux
RELEASE_ARCH := arm64

.PHONY: build test run release clean docker-build check-env deploy logs

# Build for this machine.
build:
	go build -o $(BINARY) .

# Vet and test. -race because the redirect path takes a read lock while admin
# writes take the write lock, and that split is worth proving on every run.
test:
	go vet ./...
	go test -race ./...

# Run locally against ./data, reading .env for the admin password.
run: build
	@test -f .env || { echo "no .env: cp .env.example .env and set a password"; exit 1; }
	@set -a; . ./.env; set +a; \
	TEENYURL_ADDR=127.0.0.1:8080 \
	TEENYURL_BASE_URL=http://127.0.0.1:8080 \
	TEENYURL_DATA_DIR=./data \
	./$(BINARY)

# Cross-compiled, stripped, static. -trimpath drops build machine paths;
# -s -w drop the symbol and DWARF tables.
release:
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=$(RELEASE_OS) GOARCH=$(RELEASE_ARCH) \
		go build -trimpath -ldflags="-s -w" \
		-o $(DIST)/$(BINARY)-$(RELEASE_OS)-$(RELEASE_ARCH) .

clean:
	rm -rf $(BINARY) $(DIST)

# Sanity-check that the Dockerfile builds.
docker-build:
	docker compose build

# Compare the variable names in .env against .env.example, so a variable added
# upstream is caught here rather than by a failed `docker compose up`. The
# rules are in check-env.awk.
check-env:
	@test -f .env || { echo "no .env: cp .env.example .env and fill it in"; exit 1; }
	@awk -f check-env.awk .env.example .env

# On the server. check-env runs after the pull, not as a prerequisite: the
# point is to test .env against the .env.example that just arrived.
deploy:
	git pull
	$(MAKE) check-env
	docker compose up -d --build

logs:
	docker compose logs -f teenyurl
