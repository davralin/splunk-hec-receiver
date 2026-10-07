FROM golang:1.24.4-bookworm@sha256:10f549dc8489597aa7ed2b62008199bb96717f52a8e8434ea035d5b44368f8a6 AS build

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN go test ./...
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/splunk-hec-receiver ./cmd/splunk-hec-receiver

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

LABEL org.opencontainers.image.title="splunk-hec-receiver"
LABEL org.opencontainers.image.description="Bounded HEC-compatible receiver for Splunk OpenTelemetry Collector validation"

COPY --from=build /out/splunk-hec-receiver /splunk-hec-receiver

EXPOSE 8088
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 CMD ["/splunk-hec-receiver", "healthcheck"]

USER nonroot:nonroot
ENTRYPOINT ["/splunk-hec-receiver"]
