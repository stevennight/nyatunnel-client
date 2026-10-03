# syntax=docker/dockerfile:1.7
# Headless NyaTunnel client for servers and NAS (no GUI). See README "Docker".

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG NYATUNNEL_VERSION=0.1.0-dev
ARG NYATUNNEL_COMMIT=
ARG NYATUNNEL_BUILD_DATE=
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X nyatunnel-client/internal/shared/version.Version=${NYATUNNEL_VERSION} -X nyatunnel-client/internal/shared/version.Commit=${NYATUNNEL_COMMIT} -X nyatunnel-client/internal/shared/version.BuildDate=${NYATUNNEL_BUILD_DATE}" \
    -o /out/nyatunnel ./cmd/nyatunnel

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata su-exec \
    && addgroup -S nyatunnel \
    && adduser -S -G nyatunnel nyatunnel
COPY --from=build /out/nyatunnel /usr/local/bin/nyatunnel
COPY deploy/docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENV NYATUNNEL_HOME=/data
VOLUME /data
ENTRYPOINT ["/entrypoint.sh"]
CMD ["run"]
