FROM golang:1.24 AS builder
ARG TARGETARCH
WORKDIR /go/src/github.com/sparkfabrik/http-proxy
COPY go.mod .
COPY go.sum .
# Ensure ca-certificates are available for go mod download
RUN apt-get update && apt-get install -y ca-certificates && update-ca-certificates && rm -rf /var/lib/apt/lists/*
ENV GOINSECURE=github.com,proxy.golang.org,golang.org GOSUMDB=off
RUN go mod download
COPY cmd/ ./cmd/
COPY pkg/ ./pkg/
RUN GOOS=linux GOARCH=$TARGETARCH CGO_ENABLED=0 go build -v -o join-networks ./cmd/join-networks
RUN GOOS=linux GOARCH=$TARGETARCH CGO_ENABLED=0 go build -v -o dns-server ./cmd/dns-server
RUN GOOS=linux GOARCH=$TARGETARCH CGO_ENABLED=0 go build -v -o dinghy-layer ./cmd/dinghy-layer

FROM alpine:latest
RUN apk add --no-cache ca-certificates
COPY --from=builder /go/src/github.com/sparkfabrik/http-proxy/dns-server /usr/local/bin/dns-server
COPY --from=builder /go/src/github.com/sparkfabrik/http-proxy/join-networks /usr/local/bin/join-networks
COPY --from=builder /go/src/github.com/sparkfabrik/http-proxy/dinghy-layer /usr/local/bin/dinghy-layer
EXPOSE 19322/udp