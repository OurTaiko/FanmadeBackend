FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/backup ./cmd/backup
COPY internal/backup ./internal/backup
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/ourtaiko-fanmade-backup ./cmd/backup

FROM scratch
COPY --from=build /out/ourtaiko-fanmade-backup /ourtaiko-fanmade-backup
