# rollopsd container image. Pure-Go build (modernc sqlite, no cgo) with the UI
# bundle embedded via go:embed, on a minimal Alpine base carrying kubectl — the
# Kubernetes target and traffic-router plugin drive the cluster through kubectl.
FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X go.klarlabs.de/rollops/internal/version.Version=${VERSION}" \
    -o /out/rollopsd ./cmd/rollopsd

FROM alpine:3.20
# Link this image to its source repo on GHCR (provenance + lets the repo's
# GITHUB_TOKEN inherit push access, so releases don't need a standing PAT).
LABEL org.opencontainers.image.source="https://github.com/klarlabs-studio/rollops"
ARG KUBECTL_VERSION=v1.31.0
RUN apk add --no-cache ca-certificates curl git \
 && curl -fsSLo /usr/local/bin/kubectl \
      "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" \
 && chmod +x /usr/local/bin/kubectl \
 && apk del curl \
 && adduser -D -u 10001 rollops \
 && mkdir -p /var/lib/rollops && chown rollops:rollops /var/lib/rollops
COPY --from=build /out/rollopsd /usr/local/bin/rollopsd
USER 10001
ENV ROLLOPS_DB=/var/lib/rollops/rollops.db \
    ROLLOPS_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/rollopsd"]
