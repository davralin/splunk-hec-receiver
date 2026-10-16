use std::{
    collections::VecDeque,
    env,
    io::{Cursor, Read},
    sync::{Arc, Mutex},
};

use axum::{
    Router,
    body::{Body, to_bytes},
    extract::State,
    http::{HeaderMap, Method, Request, StatusCode, header},
    response::{IntoResponse, Response},
    routing::any,
};
use flate2::read::GzDecoder;
use serde::Serialize;
use serde_json::{Value, value::RawValue};
use subtle::ConstantTimeEq;
use time::OffsetDateTime;
use tokio::sync::Semaphore;

const DEFAULT_LISTEN_ADDRESS: &str = ":8088";
const DEFAULT_MAX_REQUEST_BYTES: usize = 4 << 20;
const DEFAULT_MAX_DECODED_BYTES: usize = 16 << 20;
const DEFAULT_MAX_CAPTURE_BYTES: usize = 64 << 20;
const DEFAULT_MAX_EVENT_BYTES: usize = 1 << 20;
const DEFAULT_MAX_EVENTS: usize = 10_000;
const DEFAULT_MAX_CONCURRENT_REQUESTS: usize = 4;

#[derive(Debug, Clone)]
pub struct Config {
    pub listen_address: String,
    accepted_tokens: Vec<String>,
    max_request_bytes: usize,
    max_decoded_bytes: usize,
    max_capture_bytes: usize,
    max_event_bytes: usize,
    max_events: usize,
    max_concurrent_requests: usize,
}

impl Config {
    pub fn from_env() -> Result<Self, String> {
        let max_request_bytes = bytes("HEC_MAX_REQUEST_BYTES", DEFAULT_MAX_REQUEST_BYTES)?;
        let max_decoded_bytes = bytes("HEC_MAX_DECOMPRESSED_BYTES", DEFAULT_MAX_DECODED_BYTES)?;
        let max_capture_bytes = bytes("HEC_MAX_CAPTURE_BYTES", DEFAULT_MAX_CAPTURE_BYTES)?;
        let max_event_bytes = bytes("HEC_MAX_EVENT_BYTES", DEFAULT_MAX_EVENT_BYTES)?;
        let max_events = integer("HEC_MAX_EVENTS", DEFAULT_MAX_EVENTS)?;
        let max_concurrent_requests = integer(
            "HEC_MAX_CONCURRENT_REQUESTS",
            DEFAULT_MAX_CONCURRENT_REQUESTS,
        )?;
        if max_event_bytes > max_capture_bytes {
            return Err("HEC_MAX_EVENT_BYTES cannot exceed HEC_MAX_CAPTURE_BYTES".into());
        }
        let accepted_tokens = match env::var("HEC_ACCEPTED_TOKENS") {
            Ok(value) if !value.is_empty() => value
                .split(',')
                .map(str::trim)
                .map(str::to_owned)
                .map(|token| {
                    if token.is_empty() {
                        Err("HEC_ACCEPTED_TOKENS contains an empty token".into())
                    } else {
                        Ok(token)
                    }
                })
                .collect::<Result<Vec<_>, String>>()?,
            _ => Vec::new(),
        };
        Ok(Self {
            listen_address: env::var("HEC_LISTEN_ADDRESS")
                .ok()
                .filter(|value| !value.is_empty())
                .unwrap_or_else(|| DEFAULT_LISTEN_ADDRESS.into()),
            accepted_tokens,
            max_request_bytes,
            max_decoded_bytes,
            max_capture_bytes,
            max_event_bytes,
            max_events,
            max_concurrent_requests,
        })
    }
}

fn bytes(name: &str, fallback: usize) -> Result<usize, String> {
    match env::var(name) {
        Ok(value) if !value.is_empty() => parse_bytes(&value)
            .filter(|value| *value > 0)
            .ok_or_else(|| format!("{name} must be a positive byte size")),
        _ => Ok(fallback),
    }
}
fn integer(name: &str, fallback: usize) -> Result<usize, String> {
    match env::var(name) {
        Ok(value) if !value.is_empty() => value
            .parse()
            .ok()
            .filter(|value: &usize| *value > 0)
            .ok_or_else(|| format!("{name} must be a positive integer")),
        _ => Ok(fallback),
    }
}
fn parse_bytes(value: &str) -> Option<usize> {
    let value = value.trim().to_ascii_uppercase();
    let (number, multiplier) = [
        ("KIB", 1usize << 10),
        ("MIB", 1usize << 20),
        ("GIB", 1usize << 30),
        ("KB", 1usize << 10),
        ("MB", 1usize << 20),
        ("GB", 1usize << 30),
        ("B", 1),
    ]
    .into_iter()
    .find_map(|(suffix, multiplier)| {
        value
            .strip_suffix(suffix)
            .map(|number| (number, multiplier))
    })
    .unwrap_or((&value, 1));
    number.trim().parse::<usize>().ok()?.checked_mul(multiplier)
}

#[derive(Clone)]
struct Server {
    config: Config,
    store: Arc<Mutex<Store>>,
    requests: Arc<Semaphore>,
}
impl Server {
    fn new(config: Config) -> Self {
        Self {
            store: Arc::new(Mutex::new(Store::new(
                config.max_capture_bytes,
                config.max_events,
            ))),
            requests: Arc::new(Semaphore::new(config.max_concurrent_requests)),
            config,
        }
    }
    fn authorized(&self, headers: &HeaderMap) -> bool {
        if self.config.accepted_tokens.is_empty() {
            return true;
        }
        let Some(value) = headers
            .get(header::AUTHORIZATION)
            .and_then(|value| value.to_str().ok())
        else {
            return false;
        };
        let Some(token) = value.strip_prefix("Splunk ") else {
            return false;
        };
        self.config
            .accepted_tokens
            .iter()
            .any(|accepted| bool::from(token.as_bytes().ct_eq(accepted.as_bytes())))
    }
}

pub fn app(config: Config) -> Router {
    Router::new()
        .fallback(any(handle))
        .with_state(Arc::new(Server::new(config)))
}
async fn handle(State(server): State<Arc<Server>>, request: Request<Body>) -> Response {
    match request.uri().path() {
        "/services/collector" | "/services/collector/event" if request.method() == Method::POST => {
            ingest(server, request).await
        }
        "/services/collector" | "/services/collector/event" => {
            method_not_allowed(Method::POST, true)
        }
        "/services/collector/health" if request.method() == Method::GET => {
            hec(StatusCode::OK, "Success", 0)
        }
        "/services/collector/health" => method_not_allowed(Method::GET, true),
        "/healthz" if request.method() == Method::GET => StatusCode::OK.into_response(),
        "/healthz" => method_not_allowed(Method::GET, false),
        "/api/v1/events" => events(server, request),
        "/api/v1/status" => status(server, request),
        _ => (StatusCode::NOT_FOUND, "404 page not found\n").into_response(),
    }
}

async fn ingest(server: Arc<Server>, request: Request<Body>) -> Response {
    if !server.authorized(request.headers()) {
        return hec(StatusCode::UNAUTHORIZED, "Invalid token", 4);
    }
    let Ok(_permit) = server.requests.clone().try_acquire_owned() else {
        return hec(StatusCode::SERVICE_UNAVAILABLE, "Receiver is busy", 9);
    };
    let endpoint = request.uri().path().to_owned();
    let app_name = header_value(request.headers(), "__splunk_app_name");
    let app_version = header_value(request.headers(), "__splunk_app_version");
    let body = match read_body(&server.config, request).await {
        Ok(body) => body,
        Err(response) => return response,
    };
    let lines = split_lines(&body);
    if lines.is_empty() {
        return hec(StatusCode::BAD_REQUEST, "No data", 5);
    }
    let mut store = server.store.lock().expect("event store mutex poisoned");
    for line in lines {
        if serde_json::from_slice::<Value>(line).is_err() {
            return hec(StatusCode::BAD_REQUEST, "Invalid data format", 6);
        }
        store.add(
            endpoint.clone(),
            app_name.clone(),
            app_version.clone(),
            line,
            server.config.max_event_bytes,
        );
    }
    hec(StatusCode::OK, "Success", 0)
}
async fn read_body(config: &Config, request: Request<Body>) -> Result<Vec<u8>, Response> {
    if request
        .headers()
        .get(header::CONTENT_LENGTH)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.parse::<usize>().ok())
        .is_some_and(|length| length > config.max_request_bytes)
    {
        return Err(hec(
            StatusCode::PAYLOAD_TOO_LARGE,
            "Request body too large",
            6,
        ));
    }
    let encoding = request
        .headers()
        .get(header::CONTENT_ENCODING)
        .and_then(|value| value.to_str().ok())
        .unwrap_or_default()
        .to_owned();
    if !encoding.is_empty() && !encoding.eq_ignore_ascii_case("gzip") {
        return Err(hec(
            StatusCode::UNSUPPORTED_MEDIA_TYPE,
            "Unsupported content encoding",
            6,
        ));
    }
    let compressed = to_bytes(request.into_body(), config.max_request_bytes + 1)
        .await
        .map_err(|_| hec(StatusCode::PAYLOAD_TOO_LARGE, "Request body too large", 6))?;
    if compressed.len() > config.max_request_bytes {
        return Err(hec(
            StatusCode::PAYLOAD_TOO_LARGE,
            "Request body too large",
            6,
        ));
    }
    let mut decoded = Vec::new();
    if encoding.is_empty() {
        decoded.extend_from_slice(&compressed);
    } else {
        GzDecoder::new(Cursor::new(compressed))
            .take((config.max_decoded_bytes + 1) as u64)
            .read_to_end(&mut decoded)
            .map_err(|_| hec(StatusCode::BAD_REQUEST, "Invalid gzip data", 6))?;
    }
    if decoded.len() > config.max_decoded_bytes {
        return Err(hec(
            StatusCode::PAYLOAD_TOO_LARGE,
            "Decoded request body too large",
            6,
        ));
    }
    Ok(decoded)
}
fn header_value(headers: &HeaderMap, name: &str) -> Option<String> {
    headers
        .get(name)
        .and_then(|value| value.to_str().ok())
        .filter(|value| !value.is_empty())
        .map(str::to_owned)
}
fn split_lines(body: &[u8]) -> Vec<&[u8]> {
    body.split(|byte| matches!(*byte, b'\n' | b'\r'))
        .map(|line| line.trim_ascii())
        .filter(|line| !line.is_empty())
        .collect()
}

fn events(server: Arc<Server>, request: Request<Body>) -> Response {
    if request.method() != Method::GET {
        return method_not_allowed(Method::GET, false);
    }
    let mut after = 0;
    let mut limit = 100;
    for (name, value) in request
        .uri()
        .query()
        .unwrap_or_default()
        .split('&')
        .filter_map(|pair| pair.split_once('='))
    {
        match name {
            "after" => match value.parse() {
                Ok(value) => after = value,
                Err(_) => {
                    return plain(StatusCode::BAD_REQUEST, "after must be an unsigned integer");
                }
            },
            "limit" => match value.parse::<usize>() {
                Ok(value @ 1..=1000) => limit = value,
                _ => return plain(StatusCode::BAD_REQUEST, "limit must be between 1 and 1000"),
            },
            _ => {}
        }
    }
    json(
        StatusCode::OK,
        serde_json::json!({"events": server.store.lock().expect("event store mutex poisoned").events_after(after, limit)}),
    )
}
fn status(server: Arc<Server>, request: Request<Body>) -> Response {
    if request.method() != Method::GET {
        return method_not_allowed(Method::GET, false);
    }
    let stats = server
        .store
        .lock()
        .expect("event store mutex poisoned")
        .snapshot();
    json(
        StatusCode::OK,
        serde_json::json!({"acl_enabled": !server.config.accepted_tokens.is_empty(), "max_capture_bytes": server.config.max_capture_bytes, "max_events": server.config.max_events, "stats": stats}),
    )
}
fn method_not_allowed(method: Method, hec_body: bool) -> Response {
    let mut response = if hec_body {
        hec(StatusCode::METHOD_NOT_ALLOWED, "Method not allowed", 5)
    } else {
        plain(StatusCode::METHOD_NOT_ALLOWED, "method not allowed")
    };
    response
        .headers_mut()
        .insert(header::ALLOW, method.as_str().parse().unwrap());
    response
}
fn hec(status: StatusCode, text: &str, code: u8) -> Response {
    json(status, serde_json::json!({"code": code, "text": text}))
}
fn json(status: StatusCode, value: Value) -> Response {
    (
        status,
        [(header::CONTENT_TYPE, "application/json")],
        value.to_string(),
    )
        .into_response()
}
fn plain(status: StatusCode, message: &str) -> Response {
    (status, format!("{message}\n")).into_response()
}

#[derive(Debug, Clone, Serialize)]
struct CapturedEvent {
    id: u64,
    #[serde(with = "time::serde::rfc3339")]
    received: OffsetDateTime,
    endpoint: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    app_name: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    app_version: Option<String>,
    envelope: Box<RawValue>,
}
#[derive(Debug, Clone, Serialize)]
struct Stats {
    accepted_events: u64,
    retained_events: u64,
    retained_bytes: usize,
    evicted_events: u64,
    oversize_events: u64,
}
struct Store {
    events: VecDeque<CapturedEvent>,
    bytes: usize,
    next_id: u64,
    max_bytes: usize,
    max_events: usize,
    stats: Stats,
}
impl Store {
    fn new(max_bytes: usize, max_events: usize) -> Self {
        Self {
            events: VecDeque::new(),
            bytes: 0,
            next_id: 0,
            max_bytes,
            max_events,
            stats: Stats {
                accepted_events: 0,
                retained_events: 0,
                retained_bytes: 0,
                evicted_events: 0,
                oversize_events: 0,
            },
        }
    }
    fn add(
        &mut self,
        endpoint: String,
        app_name: Option<String>,
        app_version: Option<String>,
        envelope: &[u8],
        max_event_bytes: usize,
    ) {
        self.stats.accepted_events += 1;
        if envelope.len() > max_event_bytes || envelope.len() > self.max_bytes {
            self.stats.oversize_events += 1;
            return;
        }
        self.next_id += 1;
        self.bytes += envelope.len();
        self.events.push_back(CapturedEvent {
            id: self.next_id,
            received: OffsetDateTime::now_utc(),
            endpoint,
            app_name,
            app_version,
            envelope: RawValue::from_string(String::from_utf8(envelope.to_vec()).unwrap()).unwrap(),
        });
        while self.events.len() > self.max_events || self.bytes > self.max_bytes {
            self.bytes -= self.events.pop_front().unwrap().envelope.get().len();
            self.stats.evicted_events += 1;
        }
        self.stats.retained_events = self.events.len() as u64;
        self.stats.retained_bytes = self.bytes;
    }
    fn events_after(&self, after: u64, limit: usize) -> Vec<CapturedEvent> {
        self.events
            .iter()
            .filter(|event| event.id > after)
            .take(limit)
            .cloned()
            .collect()
    }
    fn snapshot(&self) -> Stats {
        self.stats.clone()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::body::to_bytes;
    use flate2::{Compression, write::GzEncoder};
    use std::io::Write;
    use tower::ServiceExt;
    fn config() -> Config {
        Config {
            listen_address: ":8088".into(),
            accepted_tokens: Vec::new(),
            max_request_bytes: 1024,
            max_decoded_bytes: 1024,
            max_capture_bytes: 512,
            max_event_bytes: 256,
            max_events: 2,
            max_concurrent_requests: 1,
        }
    }
    async fn send(app: Router, request: Request<Body>) -> Response {
        app.oneshot(request).await.unwrap()
    }
    fn post(path: &str, body: impl Into<Body>) -> Request<Body> {
        Request::builder()
            .method(Method::POST)
            .uri(path)
            .body(body.into())
            .unwrap()
    }
    #[tokio::test]
    async fn ingests_ndjson_and_exposes_events() {
        let app = app(config());
        let mut request = post("/services/collector/event", "{\"event\":1}\n{\"event\":2}");
        request
            .headers_mut()
            .insert("__splunk_app_name", "receiver-test".parse().unwrap());
        assert_eq!(send(app.clone(), request).await.status(), StatusCode::OK);
        let response = send(
            app,
            Request::builder()
                .uri("/api/v1/events")
                .body(Body::empty())
                .unwrap(),
        )
        .await;
        let payload: Value =
            serde_json::from_slice(&to_bytes(response.into_body(), usize::MAX).await.unwrap())
                .unwrap();
        assert_eq!(payload["events"].as_array().unwrap().len(), 2);
        assert_eq!(payload["events"][0]["app_name"], "receiver-test");
    }
    #[tokio::test]
    async fn accepts_gzip() {
        let mut encoder = GzEncoder::new(Vec::new(), Compression::default());
        encoder.write_all(b"{\"event\":1}").unwrap();
        let request = Request::builder()
            .method(Method::POST)
            .uri("/services/collector")
            .header(header::CONTENT_ENCODING, "gzip")
            .body(Body::from(encoder.finish().unwrap()))
            .unwrap();
        assert_eq!(send(app(config()), request).await.status(), StatusCode::OK);
    }
    #[tokio::test]
    async fn validates_auth_and_input() {
        for (payload, encoding, status) in [
            ("", None, StatusCode::BAD_REQUEST),
            ("{", None, StatusCode::BAD_REQUEST),
            ("bad", Some("gzip"), StatusCode::BAD_REQUEST),
            ("{}", Some("br"), StatusCode::UNSUPPORTED_MEDIA_TYPE),
        ] {
            let mut request = post("/services/collector/event", payload);
            if let Some(encoding) = encoding {
                request
                    .headers_mut()
                    .insert(header::CONTENT_ENCODING, encoding.parse().unwrap());
            }
            assert_eq!(send(app(config()), request).await.status(), status);
        }
        let mut cfg = config();
        cfg.accepted_tokens.push("test".into());
        assert_eq!(
            send(app(cfg), post("/services/collector/event", "{}"))
                .await
                .status(),
            StatusCode::UNAUTHORIZED
        );
    }
    #[tokio::test]
    async fn enforces_limits_and_eviction() {
        let mut cfg = config();
        cfg.max_event_bytes = 10;
        let app = app(cfg);
        assert_eq!(
            send(
                app.clone(),
                post("/services/collector/event", "{\"event\":1}")
            )
            .await
            .status(),
            StatusCode::OK
        );
        let response = send(
            app,
            Request::builder()
                .uri("/api/v1/status")
                .body(Body::empty())
                .unwrap(),
        )
        .await;
        let payload: Value =
            serde_json::from_slice(&to_bytes(response.into_body(), usize::MAX).await.unwrap())
                .unwrap();
        assert_eq!(payload["stats"]["oversize_events"], 1);
    }
    #[tokio::test]
    async fn supports_inspection_and_methods() {
        let app = app(config());
        for event in ["{\"event\":1}", "{\"event\":2}", "{\"event\":3}"] {
            assert_eq!(
                send(app.clone(), post("/services/collector/event", event))
                    .await
                    .status(),
                StatusCode::OK
            );
        }
        assert_eq!(
            send(
                app.clone(),
                Request::builder()
                    .uri("/api/v1/events?limit=0")
                    .body(Body::empty())
                    .unwrap()
            )
            .await
            .status(),
            StatusCode::BAD_REQUEST
        );
        let response = send(
            app.clone(),
            Request::builder()
                .uri("/api/v1/status")
                .body(Body::empty())
                .unwrap(),
        )
        .await;
        let payload: Value =
            serde_json::from_slice(&to_bytes(response.into_body(), usize::MAX).await.unwrap())
                .unwrap();
        assert_eq!(payload["stats"]["evicted_events"], 1);
        let response = send(
            app,
            Request::builder()
                .method(Method::GET)
                .uri("/services/collector")
                .body(Body::empty())
                .unwrap(),
        )
        .await;
        assert_eq!(response.status(), StatusCode::METHOD_NOT_ALLOWED);
        assert_eq!(response.headers()[header::ALLOW], "POST");
    }
    #[test]
    fn parses_sizes() {
        assert_eq!(parse_bytes("1KiB"), Some(1024));
        assert_eq!(parse_bytes("3GB"), Some(3 << 30));
        assert_eq!(parse_bytes("bad"), None);
    }
}
