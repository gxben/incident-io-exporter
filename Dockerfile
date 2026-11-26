FROM golang:1.26-alpine AS builder

ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -trimpath \
      -o /bin/incident-io-exporter \
      .

# ─────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="incident-io-exporter" \
      org.opencontainers.image.description="Prometheus exporter for incident.io" \
      org.opencontainers.image.url="https://github.com/gxben/incident-io-exporter" \
      org.opencontainers.image.source="https://github.com/gxben/incident-io-exporter"

COPY --from=builder /bin/incident-io-exporter /incident-io-exporter

EXPOSE 9429
USER nonroot:nonroot

ENTRYPOINT ["/incident-io-exporter"]
