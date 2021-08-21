FROM golang:1.16-4-alpine as build

RUN apk add --nocache git


WORKDIR /app/telego

COPY src/go.mod .
COPY src/go.sum .

RUN go mod download


COPY . .