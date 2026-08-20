# Build stage.
FROM golang:1.26-alpine AS build

WORKDIR /src

# Copy the module files first, so a source-only change does not re-download
# the one dependency.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off, because the final stage is scratch and has no libc.
# -trimpath drops the build machine's paths; -s -w drop the symbol and DWARF
# tables, which the service never needs.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /teenyurl .

# Final stage.
#
# scratch, not alpine: the server makes no outbound calls, so it needs no CA
# certificates, and every timestamp is UTC, so it needs no tzdata. The web
# assets are compiled into the binary. Nothing else belongs in the image.
FROM scratch

COPY --from=build /teenyurl /teenyurl

# 65534 is nobody. scratch has no /etc/passwd, so the id must be numeric.
# The host directory behind /data must be owned by the same id.
USER 65534:65534

EXPOSE 8080
VOLUME ["/data"]

# There is no shell in this image, so the binary checks itself.
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD ["/teenyurl", "healthcheck"]

ENTRYPOINT ["/teenyurl"]
