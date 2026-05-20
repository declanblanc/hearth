# --- build ---
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO is disabled — we use modernc.org/sqlite, a pure-Go driver, so the
# resulting binary is fully static and portable.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/hearth ./cmd/server

# --- runtime ---
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/hearth /app/hearth
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/hearth"]
