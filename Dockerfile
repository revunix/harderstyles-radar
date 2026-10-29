# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/harderstyles-radar ./cmd/harderstyles-radar

FROM alpine:3.22
# ca-certificates for the HTTPS calls, tzdata so dates and schedules follow the local timezone.
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/harderstyles-radar /usr/bin/harderstyles-radar

# The cache and an optional .env live on the volume; mount one at /data.
ENV CACHE_FILE=/data/hardstyle-sync-cache.json
WORKDIR /data

ENTRYPOINT ["/usr/bin/harderstyles-radar"]
