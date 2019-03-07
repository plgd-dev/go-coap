FROM golang:1.11-alpine3.8 AS build

RUN apk add --no-cache curl git build-base && \
	curl -SL -o /usr/bin/dep https://github.com/golang/dep/releases/download/v0.5.0/dep-linux-amd64 && \
	chmod +x /usr/bin/dep

ENV MAINDIR $GOPATH/src/github.com/go-ocf/go-coap
WORKDIR $MAINDIR
COPY Gopkg.toml Gopkg.lock ./
RUN dep ensure -v --vendor-only
COPY . .