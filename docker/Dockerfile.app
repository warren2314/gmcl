FROM golang:1.22-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git build-base

COPY go.mod ./
RUN go mod download

COPY . .

# Generate go.sum from go.mod and fetch checksums (host may not have go.sum)
RUN go mod tidy

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /bin/app ./cmd/app

FROM alpine:3.20

RUN apk add --no-cache wget

WORKDIR /

COPY --from=builder /bin/app /bin/app
COPY --from=builder /app/migrations /migrations
COPY --from=builder /app/static /static
COPY --from=builder /app/images /images

ENV APP_HTTP_ADDR=:8080
ENV MIGRATE_DIR=/migrations

EXPOSE 8080

ENTRYPOINT ["/bin/app"]

