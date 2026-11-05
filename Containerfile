FROM rust:1-slim-bookworm@sha256:7d53bccd41fde2f509a276b37a8ef541f8e75fc3e1f85b059230386f512ed6c1 AS build

WORKDIR /src
RUN apt-get update && apt-get install --yes --no-install-recommends musl-tools && rm -rf /var/lib/apt/lists/*
COPY Cargo.toml Cargo.lock ./
COPY src ./src
RUN cargo fetch --locked
RUN rustup component add rustfmt
RUN cargo fmt --check
RUN cargo test --locked
RUN target="$(rustc -vV | sed -n 's/^host: \(.*\)-gnu$/\1-musl/p')" && rustup target add "$target" && cargo build --release --locked --target "$target" && mkdir /out && cp "target/$target/release/splunk-hec-receiver" /out/splunk-hec-receiver

FROM build AS test

RUN cargo test --locked

FROM scratch

LABEL org.opencontainers.image.title="splunk-hec-receiver"
LABEL org.opencontainers.image.description="Bounded HEC-compatible receiver for Splunk OpenTelemetry Collector validation"

COPY --from=build /out/splunk-hec-receiver /splunk-hec-receiver

EXPOSE 8088
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 CMD ["/splunk-hec-receiver", "healthcheck"]

USER 65532:65532
ENTRYPOINT ["/splunk-hec-receiver"]
