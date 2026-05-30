# --- build ---
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO is disabled — we use modernc.org/sqlite, a pure-Go driver, so the
# resulting binary is fully static and portable.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/hearth ./cmd/server

# --- litestream ---
FROM debian:bookworm-slim AS litestream
ARG LITESTREAM_VERSION=0.3.13
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget \
  && wget -qO /tmp/litestream.tar.gz \
     "https://github.com/benbjohnson/litestream/releases/download/v${LITESTREAM_VERSION}/litestream-v${LITESTREAM_VERSION}-linux-amd64.tar.gz" \
  && tar -xzf /tmp/litestream.tar.gz -C /usr/local/bin litestream \
  && rm /tmp/litestream.tar.gz

# --- runtime ---
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /out/hearth /app/hearth
COPY --from=litestream /usr/local/bin/litestream /usr/local/bin/litestream
COPY litestream.yml /app/litestream.yml
COPY start.sh /app/start.sh
EXPOSE 8080
ENTRYPOINT ["/app/start.sh"]
