# kelos-spawner-beads — spawner image for TaskSpawners with a `when.beads` source.
#
# The default kelos-spawner image is distroless/static, which cannot run the
# beads CLI at all: bd is dynamically linked against glibc, and `bd init` shells
# out to the git binary to clone the Dolt remote. This variant carries both. The
# controller selects it automatically for a TaskSpawner whose `when.beads` is
# set, so installs that never use beads keep the small default image.
#
# The spawner binary is not built here — it is copied from bin/, so this image
# must be built alongside it:
#   make image WHAT="cmd/kelos-spawner kelos-spawner-beads"
#
# BEADS_VERSION is pinned deliberately, and the pin has to be real. Whichever
# version writes a Dolt hub determines its schema, and pointing a NEWER CLI at an
# older store risks a migration — migrating one clone of a shared remote forks
# the schema irrecoverably. So this must match the version that populates the
# hub, and bumping it is a compatibility decision, not a refresh.
#
# Note the upstream install.sh cannot be used to pin: it parses no version flag
# and always installs the latest release, so `install.sh --version X` silently
# yields whatever is newest. The release asset is fetched directly instead.

ARG BEADS_VERSION=1.1.2

FROM debian:stable-slim AS beads
ARG BEADS_VERSION
ARG TARGETARCH
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*
# Verify the published checksum, as the upstream installer does.
# checksums.txt names the archive with no directory component, so sha256sum -c
# has to run from the directory holding it.
RUN set -eux; \
    cd /tmp; \
    archive="beads_${BEADS_VERSION}_linux_${TARGETARCH}.tar.gz"; \
    base="https://github.com/gastownhall/beads/releases/download/v${BEADS_VERSION}"; \
    curl -fsSL "${base}/${archive}" -o "${archive}"; \
    curl -fsSL "${base}/checksums.txt" -o checksums.txt; \
    grep " ${archive}\$" checksums.txt | sha256sum -c -; \
    tar -xzf "${archive}" -C /usr/local/bin bd; \
    rm -f "${archive}" checksums.txt; \
    bd version

FROM debian:stable-slim
ARG TARGETARCH
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --uid 65532 --user-group --no-create-home --shell /usr/sbin/nologin nonroot
COPY --from=beads /usr/local/bin/bd /usr/local/bin/bd
COPY bin/kelos-spawner-linux-${TARGETARCH} /kelos-spawner
USER 65532:65532
ENTRYPOINT ["/kelos-spawner"]
