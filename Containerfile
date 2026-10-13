FROM golang:1.27.2@sha256:007a316059246276cb214df93950be77837545dc78007a73e0291a4963d5d3bf AS build

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN go test ./...
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/splunk-hec-receiver ./cmd/splunk-hec-receiver

FROM scratch

LABEL org.opencontainers.image.title="splunk-hec-receiver"
LABEL org.opencontainers.image.description="Bounded HEC-compatible receiver for Splunk OpenTelemetry Collector validation"

COPY --from=build /out/splunk-hec-receiver /splunk-hec-receiver

EXPOSE 8088
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 CMD ["/splunk-hec-receiver", "healthcheck"]

USER 65532:65532
ENTRYPOINT ["/splunk-hec-receiver"]
