# Cross-compile on the build host: with buildx multi-platform builds the Go
# toolchain runs natively and targets TARGETOS/TARGETARCH, no emulation.
FROM --platform=$BUILDPLATFORM golang:1.24 AS build
ARG TARGETOS TARGETARCH TARGETVARIANT VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /webhook-test-endpoint . \
    && mkdir /data

# distroless/static ships CA root certs (for ACME) and a nonroot user,
# nothing else — the binary is fully static.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /webhook-test-endpoint /webhook-test-endpoint
# Writable cache dir for ACME certificates (Go resolves the default
# -acme-cache under XDG_CACHE_HOME); mount a volume here to persist certs.
COPY --from=build --chown=nonroot:nonroot /data /data
ENV XDG_CACHE_HOME=/data
EXPOSE 8080 80 443
ENTRYPOINT ["/webhook-test-endpoint"]
