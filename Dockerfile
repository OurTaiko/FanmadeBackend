FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/ourtaiko-api ./cmd/server

FROM alpine:3.23
RUN apk add --no-cache ca-certificates ffmpeg libwebp-tools curl \
    && addgroup -g 10001 app && adduser -D -u 10001 -G app app \
    && mkdir -p /data/files && chown -R app:app /data
COPY --from=build /out/ourtaiko-api /usr/local/bin/ourtaiko-api
USER 10001:10001
ENV LISTEN_ADDR=0.0.0.0:8080 STORAGE_DIR=/data/files
EXPOSE 8080
ENTRYPOINT ["ourtaiko-api"]
