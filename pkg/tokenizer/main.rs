use axum::{routing::{get, post}, Json, Router};
use hyper::body::Incoming;
use hyper_util::rt::TokioIo;
use serde::{Deserialize, Serialize};
use std::sync::Arc;
use std::path::Path;
use tokenizers::Tokenizer;
use tokio::net::UnixListener;
use tower::Service;
use std::os::unix::fs::PermissionsExt;

#[derive(Deserialize)]
struct TokenizeRequest {
    text: String,
}

#[derive(Serialize)]
struct TokenizeResponse {
    count: usize,
}

#[derive(Serialize)]
struct HealthResponse {
    status: String,
    model: String,
}

const MODEL_ID: &str = "mistralai/Mistral-7B-Instruct-v0.3";
const TOKENIZER_CACHE_PATH: &str = "/tmp/mistral_tokenizer.json";

async fn load_or_download_tokenizer() -> Tokenizer {
    // Check if we have a cached copy
    if Path::new(TOKENIZER_CACHE_PATH).exists() {
        println!("Loading tokenizer from cache: {}", TOKENIZER_CACHE_PATH);
        match Tokenizer::from_file(TOKENIZER_CACHE_PATH) {
            Ok(tk) => return tk,
            Err(e) => {
                eprintln!("Failed to load cached tokenizer: {}", e);
            }
        }
    }
    
    // Download tokenizer.json from HuggingFace
    let url = format!(
        "https://huggingface.co/{}/resolve/main/tokenizer.json",
        MODEL_ID
    );
    
    println!("Downloading tokenizer from: {}", url);
    
    let response = reqwest::get(&url).await
        .expect("Failed to download tokenizer.json");
    
    if !response.status().is_success() {
        panic!("Failed to download tokenizer: HTTP {}", response.status());
    }
    
    let bytes = response.bytes().await
        .expect("Failed to read tokenizer bytes");
    
    // Cache the tokenizer
    std::fs::write(TOKENIZER_CACHE_PATH, &bytes)
        .expect("Failed to cache tokenizer");
    
    println!("Tokenizer cached to: {}", TOKENIZER_CACHE_PATH);
    
    Tokenizer::from_bytes(&bytes)
        .expect("Failed to parse tokenizer.json")
}

#[tokio::main]
async fn main() {
    println!("Loading tokenizer model: {}", MODEL_ID);
    
    // Load the Mistral tokenizer
    let tokenizer: Arc<Tokenizer> = Arc::new(load_or_download_tokenizer().await);
    println!("Tokenizer loaded successfully");

    // Build the router with shared state
    let tk_for_count = Arc::clone(&tokenizer);
    
    let app = Router::new()
        .route("/count", post(move |Json(payload): Json<TokenizeRequest>| {
            let tk: Arc<Tokenizer> = Arc::clone(&tk_for_count);
            async move {
                let encoding = tk.encode(payload.text, false).expect("Tokenization failed");
                Json(TokenizeResponse {
                    count: encoding.get_ids().len(),
                })
            }
        }))
        .route("/health", get(|| async {
            Json(HealthResponse {
                status: "ok".to_string(),
                model: MODEL_ID.to_string(),
            })
        }));

    // Unix Domain Socket path
    let socket_path = "/tmp/tokenizer.sock";
    
    // Clean up old socket if it exists
    let _ = std::fs::remove_file(socket_path);
    
    println!("Starting tokenizer sidecar on {}", socket_path);
    
    let listener = UnixListener::bind(socket_path).expect("Failed to bind to socket");
    
    // Make socket world-readable/writable
    std::fs::set_permissions(socket_path, std::fs::Permissions::from_mode(0o777))
        .expect("Failed to set socket permissions");

    // Serve using hyper with Unix socket
    loop {
        let (stream, _) = listener.accept().await.expect("Accept failed");
        let io = TokioIo::new(stream);
        
        let app_clone = app.clone();
        
        tokio::spawn(async move {
            let service = hyper::service::service_fn(move |req: hyper::Request<Incoming>| {
                let mut app = app_clone.clone();
                async move {
                    app.call(req).await
                }
            });
            
            if let Err(e) = hyper_util::server::conn::auto::Builder::new(hyper_util::rt::TokioExecutor::new())
                .serve_connection(io, service)
                .await
            {
                eprintln!("Connection error: {}", e);
            }
        });
    }
}
