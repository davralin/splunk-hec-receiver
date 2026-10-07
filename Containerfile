FROM golang:1.27.1@sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2 AS build

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN go test ./...
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/splunk-hec-receiver ./cmd/splunk-hec-receiver

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3

LABEL org.opencontainers.image.title="splunk-hec-receiver"
LABEL org.opencontainers.image.description="Bounded HEC-compatible receiver for Splunk OpenTelemetry Collector validation"

COPY --from=build /out/splunk-hec-receiver /splunk-hec-receiver

EXPOSE 8088
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 CMD ["/splunk-hec-receiver", "healthcheck"]

USER nonroot:nonroot
ENTRYPOINT ["/splunk-hec-receiver"]
