ARG GO_VERSION=1.26.5
FROM golang:${GO_VERSION}-bookworm

ARG USERNAME=vscode
ARG USER_UID=1000
ARG USER_GID=1000

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
        ca-certificates \
        curl \
        git \
        jq \
        less \
        make \
        openssh-client \
        ripgrep \
        sqlite3 \
        sudo \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid "${USER_GID}" "${USERNAME}" \
    && useradd --uid "${USER_UID}" --gid "${USER_GID}" --create-home --shell /bin/bash "${USERNAME}" \
    && echo "${USERNAME} ALL=(root) NOPASSWD:ALL" > "/etc/sudoers.d/${USERNAME}" \
    && chmod 0440 "/etc/sudoers.d/${USERNAME}" \
    && mkdir -p \
        "/home/${USERNAME}/.cache/go-build" \
        "/home/${USERNAME}/.local/state/zotui" \
        "/home/${USERNAME}/go/pkg/mod" \
    && chown -R "${USERNAME}:${USERNAME}" "/home/${USERNAME}"

ENV GOPATH=/home/vscode/go \
    GOMODCACHE=/home/vscode/go/pkg/mod \
    GOCACHE=/home/vscode/.cache/go-build \
    PATH=/home/vscode/go/bin:${PATH}

USER ${USERNAME}
WORKDIR /workspaces/openzot
