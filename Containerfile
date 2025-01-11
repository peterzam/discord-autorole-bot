ARG GOLANG_VERSION="1.23.4"

FROM docker.io/library/golang:$GOLANG_VERSION-alpine as builder
RUN apk --update add go musl-dev util-linux-dev
WORKDIR /go/src
COPY . .
RUN GOOS=linux go build -a -ldflags "-linkmode external -extldflags '-static' -s -w" -o ./app

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /go/src/app /app
ENTRYPOINT ["/app"]
