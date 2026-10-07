# 0009. Use Bounded In-Memory HEC Capture

Date: 2026-10-07

## Status

Accepted

## Context

This service validates the Splunk OpenTelemetry Collector chart's HEC exporter and exposes recently received telemetry for inspection. It is not a Splunk replacement, a persistent telemetry backend, or a multi-replica aggregation service.

Collector log volume can be substantial. Unbounded in-memory retention would make the validation target capable of exhausting its own pod memory. A Valkey sidecar would add another memory-bound process, an eviction policy, and operational complexity without providing needed persistence for this ephemeral use case.

## Decision

Keep captured HEC envelopes in a byte- and count-bounded in-memory FIFO buffer. Evict oldest retained events first. Bound compressed request size, decompressed request size, individual retained event size, and concurrent request processing independently from capture capacity.

Return successful HEC responses for valid telemetry that is accepted but cannot be retained because it exceeds the individual capture limit. Expose counters for accepted, retained, evicted, and oversized events through the inspection API.

Do not introduce persistent storage or a Valkey dependency unless a later requirement needs retention across restarts, shared capture across replicas, or a substantially larger inspection window.

## Consequences

The service remains a single restricted-compatible deployable image with predictable application-owned memory usage.

Recent events remain available for collector validation, while sustained telemetry does not cause unbounded capture growth.

Captured events are lost on restart by design.
