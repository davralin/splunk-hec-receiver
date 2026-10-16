use splunk_hec_receiver::{Config, app};
use std::{
    env,
    io::{Read, Write},
    net::TcpStream,
    process,
    time::Duration,
};

#[tokio::main]
async fn main() {
    let config = Config::from_env().unwrap_or_else(|error| {
        eprintln!("{error}");
        process::exit(1)
    });
    match env::args().nth(1).as_deref() {
        Some("healthcheck") => {
            if let Err(error) = healthcheck(&config.listen_address) {
                eprintln!("{error}");
                process::exit(1)
            }
        }
        Some(command) => {
            eprintln!("unknown command {command:?}");
            process::exit(2)
        }
        None => serve(config).await,
    }
}
async fn serve(config: Config) {
    let bind_address = if config.listen_address.starts_with(':') {
        format!("0.0.0.0{}", config.listen_address)
    } else {
        config.listen_address.clone()
    };
    let listener = tokio::net::TcpListener::bind(&bind_address)
        .await
        .unwrap_or_else(|error| {
            eprintln!("{error}");
            process::exit(1)
        });
    println!("splunk-hec-receiver listening on {}", config.listen_address);
    if let Err(error) = axum::serve(listener, app(config))
        .with_graceful_shutdown(shutdown_signal())
        .await
    {
        eprintln!("{error}");
        process::exit(1);
    }
}
async fn shutdown_signal() {
    let mut terminate = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
        .expect("install SIGTERM handler");
    tokio::select! { _ = tokio::signal::ctrl_c() => {}, _ = terminate.recv() => {} }
}
fn healthcheck(address: &str) -> Result<(), String> {
    let (host, port) = address
        .rsplit_once(':')
        .ok_or_else(|| format!("parse listen address: {address}"))?;
    let address = if host.is_empty() || host == "::" || host == "[::]" {
        format!("127.0.0.1:{port}")
    } else {
        address.into()
    };
    let mut stream = TcpStream::connect(address).map_err(|error| error.to_string())?;
    stream
        .set_read_timeout(Some(Duration::from_secs(2)))
        .map_err(|error| error.to_string())?;
    stream
        .write_all(b"GET /healthz HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
        .map_err(|error| error.to_string())?;
    let mut response = String::new();
    stream
        .read_to_string(&mut response)
        .map_err(|error| error.to_string())?;
    let status = response.lines().next().unwrap_or_default();
    if status.starts_with("HTTP/") && status.split_whitespace().nth(1) == Some("200") {
        Ok(())
    } else {
        Err(format!("health endpoint returned {status}"))
    }
}
