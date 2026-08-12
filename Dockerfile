# rollopsd container image. Pure-Go build (modernc sqlite, no cgo) with the UI
# bundle embedded via go:embed, on a minimal Alpine base carrying kubectl — the
# Kubernetes target and traffic-router plugin drive the cluster through kubectl.
# Pinned by digest, with the tag kept in the comment so a human can still read what this is.
#
# A tag is mutable: golang:1.26-alpine resolves to whatever was last pushed under that name, so
# two builds of the same commit can use different compilers and neither says so. The digest makes
# the build reproducible and makes a base-image change a reviewable diff.
#
# The cost is that a pinned digest stops receiving base-image security patches until somebody
# bumps it, which trades one supply-chain risk for another — so this lands together with the
# Dependabot config that opens that bump as a pull request. A pin without something to move it
# is worse than a tag.
#
# Index digests, not per-architecture ones: both resolve to a manifest list covering amd64 and
# arm64, so this does not quietly restrict where the image can be built.
# golang:1.26-alpine
FROM golang@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X go.klarlabs.de/rollops/internal/version.Version=${VERSION}" \
    -o /out/rollopsd ./cmd/rollopsd

# alpine:3.20
FROM alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
# Link this image to its source repo on GHCR (provenance + lets the repo's
# GITHUB_TOKEN inherit push access, so releases don't need a standing PAT).
LABEL org.opencontainers.image.source="https://github.com/klarlabs-studio/rollops"
# Who to contact about this image. Deliberately the org handle and not a mailbox:
# an address here is PII baked into every published layer, and the repo's issue
# tracker is the route that stays correct when maintainers change.
LABEL maintainer="klarlabs-studio"
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
