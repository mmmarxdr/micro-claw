FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o microagent ./cmd/microagent

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /app/microagent /usr/local/bin/microagent
RUN adduser -D -h /home/microagent microagent
USER microagent
WORKDIR /home/microagent
ENTRYPOINT ["microagent"]
