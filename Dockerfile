# --- web UI ---
FROM node:22-alpine AS web
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# --- Go server ---
FROM golang:1.25-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /mandate ./cmd/mandate

# --- runtime ---
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -h /app app && mkdir -p /app/data && chown -R app /app
WORKDIR /app
COPY --from=build /mandate /app/mandate
COPY --from=web /app/web/dist /app/web/dist
USER app
ENV PORT=8080 STATIC_DIR=/app/web/dist DATA_FILE=/app/data/mandate.json
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/api/health || exit 1
CMD ["/app/mandate"]
