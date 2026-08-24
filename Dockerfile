ARG GO_VERSION=1.27

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/sotapi ./cmd/sotapi

FROM alpine:3.22

ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=1970-01-01T00:00:00Z

LABEL org.opencontainers.image.title="SotAPI" \
      org.opencontainers.image.description="An OpenAI-compatible API powered by human answers" \
      org.opencontainers.image.source="https://github.com/Xbai-hang/sotapi" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$REVISION \
      org.opencontainers.image.created=$CREATED

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 sotapi \
    && adduser -S -D -H -u 10001 -G sotapi sotapi \
    && mkdir -p /etc/sotapi \
    && chown -R sotapi:sotapi /etc/sotapi

COPY --from=build --chown=sotapi:sotapi /out/sotapi /usr/local/bin/sotapi

USER 10001:10001

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/sotapi"]
CMD ["--config", "/etc/sotapi/config.yaml"]
