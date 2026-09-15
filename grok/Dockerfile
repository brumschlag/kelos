FROM ubuntu:24.04

ARG GO_VERSION=1.25.0

RUN apt-get update && apt-get install -y \
    make \
    curl \
    ca-certificates \
    git \
    && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y nodejs \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
       -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
       > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update \
    && apt-get install -y gh \
    && rm -rf /var/lib/apt/lists/*

RUN ARCH=$(dpkg --print-architecture) \
    && TARBALL="go${GO_VERSION}.linux-${ARCH}.tar.gz" \
    && curl -fsSL -o "/tmp/${TARBALL}" "https://dl.google.com/go/${TARBALL}" \
    && curl -fsSL -o "/tmp/${TARBALL}.sha256" "https://dl.google.com/go/${TARBALL}.sha256" \
    && echo "$(cat /tmp/${TARBALL}.sha256)  /tmp/${TARBALL}" | sha256sum -c - \
    && tar -C /usr/local -xzf "/tmp/${TARBALL}" \
    && rm "/tmp/${TARBALL}" "/tmp/${TARBALL}.sha256"

ENV PATH="/usr/local/go/bin:${PATH}"

COPY grok/kelos_entrypoint.sh /kelos_entrypoint.sh
RUN chmod +x /kelos_entrypoint.sh

COPY hack/agent-gh-wrapper.sh /usr/local/bin/gh
RUN chmod +x /usr/local/bin/gh

ARG TARGETARCH
COPY bin/kelos-capture-linux-${TARGETARCH} /kelos/kelos-capture

RUN useradd -u 61100 -m -s /bin/bash agent
RUN mkdir -p /home/agent/.grok /home/agent/.local/bin && chown -R agent:agent /home/agent

USER agent

# Install the xAI Grok Build CLI via the first-party installer. It places the
# binary at ~/.grok/bin/grok (symlinked as both `grok` and `agent`). The
# version is pinned for reproducible builds; `bash -s <version>` selects it.
ARG GROK_VERSION=1.0.30
RUN curl -fsSL https://x.ai/cli/install.sh | bash -s "${GROK_VERSION}"

ENV GOPATH="/home/agent/go"
ENV PATH="/home/agent/.grok/bin:/home/agent/.local/bin:${GOPATH}/bin:${PATH}"

# Disable the CLI's launch-time auto-update check so CI/agent runs never reach
# out to the network to update themselves. (The headless invocation has no
# --no-auto-update flag in this CLI version; this config is the equivalent.)
RUN mkdir -p /home/agent/.grok \
    && printf '[cli]\nauto_update = false\n' > /home/agent/.grok/config.toml

# Fail the build early if the CLI is not on PATH / not runnable.
RUN grok --version

WORKDIR /workspace

ENTRYPOINT ["grok"]
