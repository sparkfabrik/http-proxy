FROM golang:1.26 AS builder
ARG TARGETARCH
ARG GIT_VERSION=unknown
WORKDIR /go/src/github.com/sparkfabrik/http-proxy
COPY go.mod .
COPY go.sum .
RUN go mod download
COPY cmd/ ./cmd/
COPY pkg/ ./pkg/
RUN GOOS=linux GOARCH=$TARGETARCH CGO_ENABLED=0 go build -v -o join-networks ./cmd/join-networks
RUN GOOS=linux GOARCH=$TARGETARCH CGO_ENABLED=0 go build -v -o dns-server ./cmd/dns-server
RUN GOOS=linux GOARCH=$TARGETARCH CGO_ENABLED=0 go build -v -o dinghy-layer ./cmd/dinghy-layer
RUN GOOS=linux GOARCH=$TARGETARCH CGO_ENABLED=0 go build -v -o tailscale-peers ./cmd/tailscale-peers

FROM alpine:3.24
RUN apk add --no-cache ca-certificates

# Accept git version as build argument in final stage
ARG GIT_VERSION=unknown

COPY --from=builder /go/src/github.com/sparkfabrik/http-proxy/dns-server /usr/local/bin/dns-server
COPY --from=builder /go/src/github.com/sparkfabrik/http-proxy/join-networks /usr/local/bin/join-networks
COPY --from=builder /go/src/github.com/sparkfabrik/http-proxy/dinghy-layer /usr/local/bin/dinghy-layer
COPY --from=builder /go/src/github.com/sparkfabrik/http-proxy/tailscale-peers /usr/local/bin/tailscale-peers

# Save git version information to a file
RUN echo "${GIT_VERSION}" > /.version
