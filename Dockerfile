FROM golang:1.12-alpine3.10 AS build

RUN apk add --no-cache curl git build-base && mkdir /work
WORKDIR /work
COPY go.mod go.sum ./
RUN go mod download
COPY . /work
