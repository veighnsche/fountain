# xx is the upstream cross-compilation shim; its complete root is its interface.
# hadolint ignore=DL3006
FROM --platform=$BUILDPLATFORM data.forgejo.org/oci/xx AS xx

FROM --platform=$BUILDPLATFORM data.forgejo.org/oci/golang:1.26-alpine3.23 AS build-tools

ARG TAGS="sqlite sqlite_unlock_notify"
ENV TAGS="bindata timetzdata $TAGS"
ARG CGO_EXTRA_CFLAGS

#
# Transparently cross compile for the target platform
#
# hadolint ignore=DL3067
COPY --from=xx / /
ARG TARGETPLATFORM
# Upstream Alpine repositories select the supported package revisions.
# hadolint ignore=DL3018
RUN apk --no-cache add clang lld
RUN xx-apk --no-cache add gcc musl-dev
ENV CGO_ENABLED=1
RUN xx-go --wrap
#
# for go generate and binfmt to find
# without it the generate phase will fail with
# #19 25.04 modules/public/public_bindata.go:8: running "go": exit status 1
# #19 25.39 aarch64-binfmt-P: Could not open '/lib/ld-musl-aarch64.so.1': No such file or directory
# why exactly is it needed? where is binfmt involved?
#
RUN cp /*-alpine-linux-musl*/lib/ld-musl-*.so.1 /lib || true

# hadolint ignore=DL3018
RUN apk --no-cache add build-base git nodejs npm

WORKDIR ${GOPATH}/src/forgejo.org
COPY package.json package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund

# Cache the complete native frontend independently of backend/version changes.
FROM build-tools AS frontend
COPY Makefile webpack.config.js tailwind.config.js ./
COPY web_src ./web_src
COPY sdk/navigationpolicy/policy.json ./sdk/navigationpolicy/policy.json
COPY public ./public
COPY assets ./assets
RUN make frontend

FROM build-tools AS build-env
ARG GOPROXY
ENV GOPROXY=${GOPROXY:-https://proxy.golang.org,direct}
COPY go.mod go.sum ./
COPY sdk ./sdk
COPY tools/fountain-toolchain.sh ./tools/fountain-toolchain.sh
# The base image can advance within its minor release; use the shared exact pin.
ENV GOTOOLCHAIN=""
RUN . ./tools/fountain-toolchain.sh \
    && selected=$(fountain_go_toolchain "$PWD") \
    && go env -w GOTOOLCHAIN="$selected"
COPY third_party ./third_party
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .

RUN make clean-no-bindata
COPY --from=frontend /go/src/forgejo.org/public ./public
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build contrib/environment-to-ini/environment-to-ini.go && xx-verify environment-to-ini
ARG RELEASE_VERSION
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    LDFLAGS="-buildid=" make FORGEJO_GENERATE_SKIP_HASH=true RELEASE_VERSION=$RELEASE_VERSION GOFLAGS="-trimpath" go-check generate-backend static-executable && xx-verify gitea

# Copy local files
COPY docker/root /tmp/local

# Set permissions
RUN chmod 755 /tmp/local/usr/bin/entrypoint \
              /tmp/local/usr/local/bin/gitea \
              /tmp/local/etc/s6/gitea/* \
              /tmp/local/etc/s6/openssh/* \
              /tmp/local/etc/s6/.s6-svscan/* \
              /go/src/forgejo.org/gitea \
              /go/src/forgejo.org/environment-to-ini
RUN chmod 644 /go/src/forgejo.org/contrib/autocompletion/bash_autocomplete

FROM data.forgejo.org/oci/alpine:3.23
ARG RELEASE_VERSION
ARG SOURCE_REVISION
ARG SOURCE_URL=https://codeberg.org/forgejo/forgejo
LABEL maintainer="contact@forgejo.org" \
      org.opencontainers.image.authors="Forgejo" \
      org.opencontainers.image.url="https://forgejo.org" \
      org.opencontainers.image.documentation="https://forgejo.org/download/#container-image" \
      org.opencontainers.image.source="${SOURCE_URL}" \
      org.opencontainers.image.revision="${SOURCE_REVISION}" \
      org.opencontainers.image.version="${RELEASE_VERSION}" \
      org.opencontainers.image.vendor="Forgejo" \
      org.opencontainers.image.licenses="GPL-3.0-or-later" \
      org.opencontainers.image.title="Forgejo. Beyond coding. We forge." \
      org.opencontainers.image.description="Forgejo is a self-hosted lightweight software forge. Easy to install and low maintenance, it just does the job."

EXPOSE 22 3000

# hadolint ignore=DL3018
RUN apk --no-cache add \
    bash \
    ca-certificates \
    curl \
    gettext \
    git \
    linux-pam \
    openssh \
    s6 \
    sqlite \
    su-exec \
    gnupg \
    && rm -rf /var/cache/apk/*

SHELL ["/bin/ash", "-eo", "pipefail", "-c"]
RUN addgroup \
    -S -g 1000 \
    git && \
  adduser \
    -S -H -D \
    -h /data/git \
    -s /bin/bash \
    -u 1000 \
    -G git \
    git && \
  echo "git:*" | chpasswd -e

ENV USER=git
ENV GITEA_CUSTOM=/data/gitea

VOLUME ["/data"]

ENTRYPOINT ["/usr/bin/entrypoint"]
CMD ["/usr/bin/s6-svscan", "/etc/s6"]

COPY --from=build-env /tmp/local /
RUN ln -s /usr/local/bin/gitea /usr/local/bin/forgejo
COPY --from=build-env /go/src/forgejo.org/gitea /app/gitea/gitea
RUN ln -s /app/gitea/gitea /app/gitea/forgejo-cli
COPY --from=build-env /go/src/forgejo.org/environment-to-ini /usr/local/bin/environment-to-ini
COPY --from=build-env /go/src/forgejo.org/contrib/autocompletion/bash_autocomplete /etc/profile.d/gitea_bash_autocomplete.sh
COPY LICENSE /usr/share/licenses/fountain/LICENSE
