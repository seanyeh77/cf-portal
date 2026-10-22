# syntax=docker/dockerfile:1

# 1. Front end
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# 2. Go binary with the front end embedded
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY testdata ./testdata
COPY --from=web /src/internal/webui/dist/app ./internal/webui/dist/app
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/portal ./cmd/portal \
 && mkdir -p /out/data

# 3. Runtime: static binary, non-root
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/portal /portal
COPY --from=build --chown=10001:10001 /out/data /data
USER 10001:10001
ENV DB_PATH=/data/portal.db LISTEN_ADDR=:8080
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/portal", "health"]
ENTRYPOINT ["/portal"]
CMD ["serve"]
