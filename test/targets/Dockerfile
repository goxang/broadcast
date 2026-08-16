# Build stage
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY test/targets/ ./test/targets/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/broadcast-target ./test/targets

# Runtime stage
FROM scratch
USER 65532:65532
COPY --from=build /out/broadcast-target /broadcast-target
ENTRYPOINT ["/broadcast-target"]
