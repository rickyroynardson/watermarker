# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

FROM golang:1.27-alpine AS migrations
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go install -tags='no_clickhouse no_mssql no_azuresql no_mysql no_sqlite3 no_ydb no_libsql no_vertica no_tidb no_redshift no_turso no_starrocks no_duckdb' \
    github.com/pressly/goose/v3/cmd/goose@v3.28.0

FROM alpine:3.23
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/ /usr/local/bin/
COPY --from=migrations /go/bin/goose /usr/local/bin/goose
COPY migrations ./migrations
USER 65532:65532
EXPOSE 8080
CMD ["api"]
