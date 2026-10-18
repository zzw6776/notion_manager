# syntax=docker/dockerfile:1

# Stage 1: Build Frontend Assets
FROM --platform=$BUILDPLATFORM node:20-alpine AS frontend-builder
WORKDIR /app/web

COPY web/package.json web/package-lock.json ./
RUN npm ci

COPY web/ ./
RUN npm run build

# Stage 2: Build Backend Binary
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS backend-builder
WORKDIR /app

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=frontend-builder /app/web/dist ./internal/web/dist

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w" \
    -o /app/notion-manager \
    ./cmd/notion-manager

# Stage 3: Minimal Runtime
FROM alpine:3.21

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=backend-builder /app/notion-manager /app/notion-manager
COPY example.config.yaml /app/example.config.yaml

RUN mkdir -p /app/accounts

ENV PORT=8081 \
    ACCOUNTS_DIR=/app/accounts

EXPOSE 8081
VOLUME ["/app/accounts"]

ENTRYPOINT ["/app/notion-manager"]
