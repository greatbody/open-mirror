FROM golang:1.23-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o open-mirror .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
    && addgroup -S mirror \
    && adduser -S -G mirror mirror \
    && mkdir -p /var/cache/open-mirror \
    && chown mirror:mirror /var/cache/open-mirror

COPY --from=builder /build/open-mirror /usr/local/bin/open-mirror
COPY config.yaml /etc/open-mirror/config.yaml

USER mirror
VOLUME /var/cache/open-mirror
EXPOSE 8080

ENTRYPOINT ["open-mirror"]
CMD ["-config", "/etc/open-mirror/config.yaml"]
