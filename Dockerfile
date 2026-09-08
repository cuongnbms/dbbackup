FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine3.24 AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -trimpath -ldflags="-s -w" -o /out/backup .

# Pin the runtime image so pg_dump/pg_restore versions are reproducible.
# pg_dump 18 can dump any server from 9.2 up to 18; bump this when your server is newer.
FROM alpine:3.24

RUN apk --no-cache add postgresql18-client gnupg tzdata

WORKDIR /app

COPY config.yaml ./
COPY --from=builder /out/backup /usr/bin/backup
RUN mkdir -p /backup

CMD ["backup"]
