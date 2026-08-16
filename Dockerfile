# Build stage
FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache dependencies.
COPY go.mod go.sum ./
RUN go mod download

# Build a fully static binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/broadcast ./cmd/broadcast

# Runtime stage
FROM scratch
USER 65532:65532
COPY --from=build /out/broadcast /broadcast
ENTRYPOINT ["/broadcast"]
