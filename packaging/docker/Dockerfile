# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG VERSION=0.1.0-dev
ARG COMMIT=container
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w \
      -X github.com/tash/tash/internal/version.Version=${VERSION} \
      -X github.com/tash/tash/internal/version.Commit=${COMMIT} \
      -X github.com/tash/tash/internal/version.BuildDate=${BUILD_DATE}" \
    -o /out/tash ./cmd/tash

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S tash \
    && adduser -S -G tash -h /data tash \
    && mkdir -p /data/tash /data/Tash/NAS \
    && chown -R tash:tash /data

COPY --from=build /out/tash /usr/local/bin/tash

ENV HOME=/data \
    TASH_HOME=/data/tash

USER tash
VOLUME ["/data"]
EXPOSE 8787 8788

ENTRYPOINT ["tash"]
CMD ["dashboard"]
