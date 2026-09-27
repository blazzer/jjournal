FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/journal ./cmd/journal

FROM alpine:3.22
RUN apk add --no-cache ca-certificates su-exec \
    && adduser -D -H -u 10001 journal
COPY --from=build /out/journal /usr/local/bin/journal
COPY docker-entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 8080
ENTRYPOINT ["/entrypoint.sh"]
