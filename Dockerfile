# Stage 1: frontend, built once on the native build platform.
FROM --platform=$BUILDPLATFORM node:24-alpine AS frontend
WORKDIR /app/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN mkdir -p /app/internal/webui/dist
COPY internal/webui/dist/.gitkeep /app/internal/webui/dist/.gitkeep
RUN npm run build

# Stage 2: Go binary, cross-compiled on the native build platform.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder
WORKDIR /app
ENV GOTOOLCHAIN=local \
    CGO_ENABLED=0
RUN apk add --no-cache ca-certificates tzdata
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY --from=frontend /app/internal/webui/dist ./internal/webui/dist
ARG VERSION=docker
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM=${TARGETVARIANT#v} go build \
    -trimpath -ldflags="-s -w -X github.com/t0mer/cic/internal/version.Version=${VERSION}" \
    -o /app/cic ./cmd/cic
# scratch has no shell: pre-create the data dir owned by the non-root user.
RUN mkdir -p /data && chown 65532:65532 /data

# Stage 3: runtime
FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /app/cic /cic
COPY --from=builder --chown=65532:65532 /data /data
USER 65532:65532
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/cic", "healthcheck"]
ENTRYPOINT ["/cic"]
CMD ["serve"]
