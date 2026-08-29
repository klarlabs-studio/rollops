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
# TAG AND DIGEST, both, and the tag is not decoration.
#
# A bare `FROM golang@sha256:...` gives Dependabot no variant to stay within, so
# it resolves the bare name — `latest`, which is Debian — and writes that digest
# back. That is #152: the base moved from Alpine to Debian under a builder that
# runs `apk`, and every image build failed with "apk: not found" for three weeks
# under a commit message reading only "bump golang from 70b4654 to 0d1d3a7".
#
# With the tag present the updater can only move the digest WITHIN that tag, so
# the distro cannot change underneath. Naming it also gives a reviewer something
# to check; a bare digest swap is unreadable by design.
FROM golang:1.26-alpine@sha256:28d89ee9cc0ff9fec75c82ca201e6bf7fdf9a679d4b7b24dfa04f2bb766bb468 AS build
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
# Tag + digest, same reasoning as the builder above. alpine:3.24 resolves to
# exactly this digest today, so naming it changes nothing about what is built —
# it only stops a future bump from moving the base somewhere else.
FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
# Supplied by BuildKit for the platform being built. Declared here because ARG scope is
# per-stage: without this line TARGETARCH is empty in this stage and the kubectl download
# below would silently build a URL with a missing path segment.
ARG TARGETARCH
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
# One checksum per architecture, because the binary differs per architecture.
#
# This used to fetch linux/amd64 unconditionally, which made the image unrunnable on an arm64
# node: the amd64 kubectl crashes the Go runtime under emulation with "fatal error:
# lfstack.push", and the failure surfaces at the first reconcile rather than at startup. The
# base images are multi-arch, so the image built and advertised portability its contents did
# not have.
#
# Selected by TARGETARCH rather than by uname, so a cross-build produces the kubectl for the
# platform it is building *for* and not for the machine doing the building — which is the
# whole point on a CI runner emitting arm64 images from an amd64 host.
#
# An unrecognized architecture fails the build loudly. The alternative, defaulting to amd64,
# reproduces exactly the bug this replaces and hides it one layer deeper.
#
# To bump: set the version and both checksums, from
# https://dl.k8s.io/release/<version>/bin/linux/<arch>/kubectl.sha256
ARG KUBECTL_VERSION=v1.31.14
ARG KUBECTL_SHA256_AMD64=8791ec7c8966b61420d55103a5fb948de9f0ca3d7306d789734975ad9704bdb0
ARG KUBECTL_SHA256_ARM64=3abb0c2d7121e1833831f56fd857a93de386e76d14b64baf86220d0afe495209
RUN apk add --no-cache ca-certificates curl git \
 && case "${TARGETARCH}" in \
      amd64) KUBECTL_SHA256="${KUBECTL_SHA256_AMD64}" ;; \
      arm64) KUBECTL_SHA256="${KUBECTL_SHA256_ARM64}" ;; \
      *) echo "unsupported TARGETARCH [${TARGETARCH}]: add its kubectl checksum" >&2; exit 1 ;; \
    esac \
 && curl -fsSLo /usr/local/bin/kubectl \
      "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
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
