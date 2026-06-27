# syntax=docker/dockerfile:1

# --- build stage: compile a static, stripped binary ---
FROM golang:1.26 AS build
WORKDIR /src

# Cache module downloads separately from the source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
# CGO disabled => fully static; -s -w strips debug info; -trimpath drops build paths.
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X github.com/khr4/tandoor-mcp/internal/server.Version=${VERSION}" \
      -o /tandoor-mcp .

# --- runtime stage: scratch (nothing but the binary + CA certs) ---
FROM scratch
LABEL org.opencontainers.image.source="https://github.com/khr4/tandoor-mcp" \
      org.opencontainers.image.description="MCP server for the Tandoor Recipes API" \
      org.opencontainers.image.licenses="MIT"

# CA roots so the client can verify HTTPS to a Tandoor instance.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /tandoor-mcp /tandoor-mcp

# Run unprivileged (numeric uid; scratch has no /etc/passwd).
USER 65532:65532
ENTRYPOINT ["/tandoor-mcp"]
