# Dockerfile for v0.5.0 Task #72.
# User instruction: "按照0.3.0, 0.4.0, 0.5.0顺序全面实施" — PLAN.md: "deploy/".
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o fusion-gateway ./cmd/gateway

FROM alpine:3.19
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /app/fusion-gateway .
EXPOSE 8100
ENTRYPOINT ["./fusion-gateway"]
CMD ["--config", "/etc/fusion-gateway/config.yaml"]
