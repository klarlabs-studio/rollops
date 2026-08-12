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
# kubectl is verified against a pinned sha256, not merely downloaded.
#
# This binary is what drives the user's cluster: whatever lands here applies
# manifests, patches HTTPRoutes and shifts production traffic. An unverified
# download makes the image's integrity depend on the CDN object being what it was
# when the tag was written.
#
# Pinned rather than fetched from the adjacent .sha256 file, because a checksum
# retrieved from the same host as the artifact is not an independent check — a host
# able to serve a substituted binary can serve a matching digest with it. Pinning
# moves the trust decision to a reviewed commit.
#
# Rollops already holds plugin binaries to exactly this standard
# (pluginhost.VerifyArtifact refuses a plugin whose sha256 does not match the pin),
# so the image was the one place shipping an unpinned executable.
#
# Written to a file and checked, rather than piped into sha256sum. The verification is
# identical either way, but a pipe on a RUN line that also mentions curl matches the
# "remote script piped directly to shell" pattern (nox IAC-023, CWE-94) — and that rule is
# worth keeping sharp for the case where it is right, which is more valuable than winning
# the argument here with a waiver.
#
# To bump: set both, from https://dl.k8s.io/release/<version>/bin/linux/amd64/kubectl.sha256
ARG KUBECTL_VERSION=v1.31.0
ARG KUBECTL_SHA256=7c27adc64a84d1c0cc3dcf7bf4b6e916cc00f3f576a2dbac51b318d926032437
RUN apk add --no-cache ca-certificates curl git \
 && curl -fsSLo /usr/local/bin/kubectl \
      "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" \
 && echo "${KUBECTL_SHA256}  /usr/local/bin/kubectl" > /tmp/kubectl.sha256 \
 && sha256sum -c /tmp/kubectl.sha256 \
 && rm /tmp/kubectl.sha256 \
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
