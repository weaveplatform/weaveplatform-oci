# weave-zot: upstream zot preconfigured for weave guest artifacts
# (docs/research/decisions/0011 and 0012). The upstream image is distroless,
# so the static weaveoci binary provides the container health check.
ARG ZOT_IMAGE=ghcr.io/project-zot/zot:v2.1.21@sha256:6b69512c00dceaad05b1144e6079aac6aa7309d7fd200f9947ecb1de09cf48c8
ARG GO_IMAGE=golang:1.27

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS weaveoci
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=devel
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X github.com/deploymenttheory/weaveplatform-oci/internal/buildinfo.version=${VERSION}" \
      -o /out/weaveoci ./cmd/weaveoci

FROM ${ZOT_IMAGE}
COPY deploy/zot/config/private.json /etc/zot/config.json
COPY deploy/zot/config/private.json /etc/zot/roles/private.json
COPY deploy/zot/config/mirror.json /etc/zot/roles/mirror.json
COPY --from=weaveoci /out/weaveoci /usr/local/bin/weaveoci
EXPOSE 5000
VOLUME ["/var/lib/registry"]
HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/weaveoci", "healthcheck", "--url", "http://127.0.0.1:5000/readyz"]
LABEL org.opencontainers.image.title="weave-zot" \
      org.opencontainers.image.source="https://github.com/deploymenttheory/weaveplatform-oci" \
      org.opencontainers.image.description="zot registry preconfigured for weave guest artifacts" \
      org.opencontainers.image.licenses="MIT AND Apache-2.0"
