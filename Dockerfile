FROM golang:1.24 AS builder
ARG TARGETARCH
WORKDIR /go/src/github.com/sparkfabrik/http-proxy
COPY go.mod .
COPY go.sum .
RUN go mod download
COPY cmd/ ./cmd/
COPY pkg/ ./pkg/
RUN GOOS=linux GOARCH=$TARGETARCH CGO_ENABLED=0 go build -v -o join-networks ./cmd/join-networks
RUN GOOS=linux GOARCH=$TARGETARCH CGO_ENABLED=0 go build -v -o dns-server ./cmd/dns-server

FROM jwilder/nginx-proxy:1.7-alpine
LABEL Author="Brian Palmer <brian@codekitchen.net>"

RUN apk upgrade --no-cache \
     && apk add --no-cache --virtual=run-deps \
     su-exec \
     curl \
     bind-tools \
     && rm -rf /tmp/* \
     /var/cache/apk/* \
     /var/tmp/*

COPY --from=builder /go/src/github.com/sparkfabrik/http-proxy/join-networks /app/join-networks
COPY --from=builder /go/src/github.com/sparkfabrik/http-proxy/dns-server /app/dns-server

COPY Procfile /app/

# override nginx configs
COPY dinghy.nginx.conf /etc/nginx/conf.d/

# override nginx-proxy templating
COPY nginx.tmpl Procfile reload-nginx /app/

COPY htdocs /var/www/default/htdocs/

ENV DOMAIN_TLD=loc
ENV DNS_IP=127.0.0.1
ENV DNS_PORT=19322

EXPOSE 19322
