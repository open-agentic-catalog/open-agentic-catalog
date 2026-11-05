use reqwest::header::{AUTHORIZATION, CONTENT_ENCODING, CONTENT_LENGTH, HeaderMap, HeaderValue};
use serde::de::DeserializeOwned;
use std::{fmt, future::Future, pin::Pin, sync::Arc, time::Duration};
use url::Url;

/// Future returned by an explicit credential provider or injected transport.
pub type BoxFuture<'a, T> = Pin<Box<dyn Future<Output = T> + Send + 'a>>;
/// The application supplies credentials; the SDK does not log in or refresh them.
pub type TokenProvider = Arc<dyn Fn() -> BoxFuture<'static, Result<String, Error>> + Send + Sync>;

/// Safe diagnostic code without a dependency error, URL, token or response body.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Error {
    InvalidConfig,
    InvalidRequest,
    InvalidCredential,
    CredentialFailed,
    RequestFailed,
    Timeout,
    RequestTooLarge,
    ResponseTooLarge,
    HeadersTooLarge,
    UnsupportedEncoding,
    DecodeFailed,
}
impl Error {
    pub(crate) fn serialization_error(_: impl fmt::Display) -> Self {
        Self::InvalidRequest
    }
}
impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "Catalog SDK: {self:?}")
    }
}
impl std::error::Error for Error {}

/// Finite budgets for one call, including credential acquisition and body reads.
#[derive(Clone, Copy, Debug)]
pub struct Limits {
    pub timeout: Duration,
    pub request_bytes: usize,
    pub response_bytes: usize,
    pub header_bytes: usize,
}
impl Default for Limits {
    fn default() -> Self {
        Self {
            timeout: Duration::from_secs(30),
            request_bytes: 16 << 20,
            response_bytes: 8 << 20,
            header_bytes: 32 << 10,
        }
    }
}
impl Limits {
    fn validate(self) -> Result<Self, Error> {
        if self.timeout.is_zero()
            || self.timeout > Duration::from_secs(86400)
            || [self.request_bytes, self.response_bytes, self.header_bytes]
                .iter()
                .any(|n| *n == 0 || *n > 1 << 30)
        {
            return Err(Error::InvalidConfig);
        }
        Ok(self)
    }
}

/// Explicit connection inputs. Debug formatting omits all identity and trust data.
pub struct Config {
    pub origin: String,
    pub allow_loopback_http: bool,
    pub token: Option<String>,
    pub token_provider: Option<TokenProvider>,
    pub root_certificates: Vec<Vec<u8>>,
    pub limits: Limits,
}
impl Config {
    pub fn new(origin: impl Into<String>) -> Self {
        Self {
            origin: origin.into(),
            allow_loopback_http: false,
            token: None,
            token_provider: None,
            root_certificates: Vec::new(),
            limits: Limits::default(),
        }
    }
}
impl fmt::Debug for Config {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Config")
            .field("limits", &self.limits)
            .finish_non_exhaustive()
    }
}

/// Exact bounded HTTP result. Formatting never reveals headers or private bytes.
pub struct Response<T> {
    pub status: u16,
    pub headers: HeaderMap,
    pub raw_body: Vec<u8>,
    /// None for a declared HTTP 304 or another explicitly empty typed result.
    pub data: Option<T>,
}
impl<T> Response<T> {
    pub(crate) fn new(data: T, status: u16, headers: HeaderMap, raw_body: Vec<u8>) -> Self {
        Self {
            data: Some(data),
            status,
            headers,
            raw_body,
        }
    }
    pub(crate) fn empty(status: u16, headers: HeaderMap, raw_body: Vec<u8>) -> Self {
        Self {
            data: None,
            status,
            headers,
            raw_body,
        }
    }
    /// Decode exact bytes explicitly without discarding response metadata.
    pub fn json<U: DeserializeOwned>(&self) -> Result<U, Error> {
        serde_json::from_slice(&self.raw_body).map_err(|_| Error::DecodeFailed)
    }
}
impl<T> fmt::Debug for Response<T> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Response")
            .field("status", &self.status)
            .finish_non_exhaustive()
    }
}

/// Explicit response evidence, including a typed problem when it can be decoded.
/// Read these fields deliberately; Display/Debug include only the status.
pub struct ApiError<E> {
    pub status: u16,
    pub headers: HeaderMap,
    pub body: String,
    pub raw_body: Vec<u8>,
    pub typed: Option<E>,
    pub parse_error: Option<String>,
}
impl<E> fmt::Debug for ApiError<E> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "Catalog SDK: HTTP {}", self.status)
    }
}
impl<E> fmt::Display for ApiError<E> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        fmt::Debug::fmt(self, f)
    }
}
impl<E> std::error::Error for ApiError<E> {}
pub enum ApiOpError<E> {
    Transport(Error),
    Api(Box<ApiError<E>>),
}
impl<E> ApiOpError<E> {
    pub(crate) fn api(error: ApiError<E>) -> Self {
        Self::Api(Box::new(error))
    }
}
impl<E> From<Error> for ApiOpError<E> {
    fn from(value: Error) -> Self {
        Self::Transport(value)
    }
}
impl<E> fmt::Debug for ApiOpError<E> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Transport(e) => fmt::Display::fmt(e, f),
            Self::Api(e) => fmt::Display::fmt(e, f),
        }
    }
}
impl<E> fmt::Display for ApiOpError<E> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        fmt::Debug::fmt(self, f)
    }
}
impl<E> std::error::Error for ApiOpError<E> {}

/// Bounded response supplied by a transport. Custom transports must bound reads.
pub struct BufferedResponse {
    pub status: u16,
    pub headers: HeaderMap,
    pub body: Vec<u8>,
}
impl BufferedResponse {
    pub(crate) fn status(&self) -> reqwest::StatusCode {
        // send() validates this before exposing it to generated methods.
        reqwest::StatusCode::from_u16(self.status).expect("validated HTTP status")
    }
    pub(crate) fn headers(&self) -> &HeaderMap {
        &self.headers
    }
}
/// Optional application-owned transport. It receives only validated fixed-origin requests.
pub trait Transport: Send + Sync {
    fn send<'a>(
        &'a self,
        request: reqwest::Request,
        limits: Limits,
    ) -> BoxFuture<'a, Result<BufferedResponse, Error>>;
}
struct HttpTransport(reqwest::Client);
impl Transport for HttpTransport {
    fn send<'a>(
        &'a self,
        request: reqwest::Request,
        limits: Limits,
    ) -> BoxFuture<'a, Result<BufferedResponse, Error>> {
        Box::pin(async move {
            let mut response = self
                .0
                .execute(request)
                .await
                .map_err(|_| Error::RequestFailed)?;
            validate_headers(response.headers(), limits)?;
            if response
                .content_length()
                .is_some_and(|n| n > limits.response_bytes as u64)
            {
                return Err(Error::ResponseTooLarge);
            }
            let mut body = Vec::new();
            while let Some(chunk) = response.chunk().await.map_err(|_| Error::RequestFailed)? {
                if chunk.len() > limits.response_bytes - body.len() {
                    return Err(Error::ResponseTooLarge);
                }
                body.extend_from_slice(&chunk);
            }
            Ok(BufferedResponse {
                status: response.status().as_u16(),
                headers: response.headers().clone(),
                body,
            })
        })
    }
}

/// All generated operations share this boundary. Clone with different budgets for one call.
#[derive(Clone)]
pub struct Client {
    pub(crate) base_url: String,
    pub(crate) http_client: reqwest::Client,
    origin: Url,
    token: Option<HeaderValue>,
    provider: Option<TokenProvider>,
    limits: Limits,
    transport: Arc<dyn Transport>,
}
impl fmt::Debug for Client {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Client")
            .field("limits", &self.limits)
            .finish_non_exhaustive()
    }
}
impl Client {
    pub fn new(config: Config) -> Result<Self, Error> {
        Self::build(config, None)
    }
    pub fn with_transport(config: Config, transport: Arc<dyn Transport>) -> Result<Self, Error> {
        Self::build(config, Some(transport))
    }
    fn build(config: Config, transport: Option<Arc<dyn Transport>>) -> Result<Self, Error> {
        let limits = config.limits.validate()?;
        let origin = origin(&config.origin, config.allow_loopback_http)?;
        if config.token.is_some() && config.token_provider.is_some() {
            return Err(Error::InvalidConfig);
        }
        let token = config
            .token
            .as_deref()
            .map(credential)
            .transpose()?
            .flatten();
        if config.root_certificates.len() > 32 {
            return Err(Error::InvalidConfig);
        }
        let mut roots = Vec::new();
        for pem in &config.root_certificates {
            if pem.len() > 1 << 20 {
                return Err(Error::InvalidConfig);
            }
            let certificates =
                reqwest::Certificate::from_pem_bundle(pem).map_err(|_| Error::InvalidConfig)?;
            if certificates.is_empty() {
                return Err(Error::InvalidConfig);
            }
            roots.extend(certificates);
        }
        let http_client = reqwest::Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .retry(reqwest::retry::never())
            .no_proxy()
            .referer(false)
            .no_gzip()
            .no_brotli()
            .no_zstd()
            .no_deflate()
            .http1_max_headers(256)
            .tls_certs_merge(roots)
            .build()
            .map_err(|_| Error::InvalidConfig)?;
        let transport = transport.unwrap_or_else(|| Arc::new(HttpTransport(http_client.clone())));
        Ok(Self {
            base_url: origin.as_str().trim_end_matches('/').to_owned(),
            origin,
            http_client,
            token,
            provider: config.token_provider,
            limits,
            transport,
        })
    }
    pub fn with_limits(&self, limits: Limits) -> Result<Self, Error> {
        Ok(Self {
            limits: limits.validate()?,
            ..self.clone()
        })
    }
    pub(crate) async fn send(
        &self,
        builder: reqwest::RequestBuilder,
    ) -> Result<BufferedResponse, Error> {
        let mut request = builder.build().map_err(|_| Error::InvalidRequest)?;
        if request.url().origin() != self.origin.origin()
            || request.url().fragment().is_some()
            || !request.url().username().is_empty()
            || request.url().password().is_some()
        {
            return Err(Error::InvalidRequest);
        }
        if request.body().is_some_and(|body| {
            body.as_bytes()
                .is_none_or(|b| b.len() > self.limits.request_bytes)
        }) {
            return Err(Error::RequestTooLarge);
        }
        if request.headers().contains_key(AUTHORIZATION)
            || request.headers().contains_key("cookie")
            || request.headers().contains_key("proxy-authorization")
            || request.headers().contains_key("host")
        {
            return Err(Error::InvalidRequest);
        }
        request
            .headers_mut()
            .insert("accept-encoding", HeaderValue::from_static("identity"));
        tokio::time::timeout(self.limits.timeout, async {
            let token = if let Some(provider) = &self.provider {
                credential(&provider().await.map_err(|_| Error::CredentialFailed)?)?
            } else {
                self.token.clone()
            };
            if let Some(token) = token {
                request.headers_mut().insert(AUTHORIZATION, token);
            }
            let response = self.transport.send(request, self.limits).await?;
            reqwest::StatusCode::from_u16(response.status).map_err(|_| Error::RequestFailed)?;
            validate_headers(&response.headers, self.limits)?;
            if response.body.len() > self.limits.response_bytes {
                return Err(Error::ResponseTooLarge);
            }
            Ok(response)
        })
        .await
        .map_err(|_| Error::Timeout)?
    }
    /// Make one fixed-origin call and preserve every HTTP status and exact bytes.
    pub async fn raw(
        &self,
        method: &str,
        path: &str,
        headers: HeaderMap,
        body: Vec<u8>,
    ) -> Result<Response<Vec<u8>>, Error> {
        if !path.starts_with('/') || path.starts_with("//") || path.contains(['\\', '#']) {
            return Err(Error::InvalidRequest);
        }
        let method =
            reqwest::Method::from_bytes(method.as_bytes()).map_err(|_| Error::InvalidRequest)?;
        let response = self
            .send(
                self.http_client
                    .request(method, format!("{}{}", self.base_url, path))
                    .headers(headers)
                    .body(body),
            )
            .await?;
        Ok(Response::new(
            response.body.clone(),
            response.status,
            response.headers,
            response.body,
        ))
    }
}
fn credential(token: &str) -> Result<Option<HeaderValue>, Error> {
    if token.len() > 32 << 10 || token.bytes().any(|b| b <= 32 || b >= 127) {
        return Err(Error::InvalidCredential);
    }
    if token.is_empty() {
        return Ok(None);
    }
    let mut value =
        HeaderValue::from_str(&format!("Bearer {token}")).map_err(|_| Error::InvalidCredential)?;
    value.set_sensitive(true);
    Ok(Some(value))
}
fn origin(value: &str, loopback: bool) -> Result<Url, Error> {
    if value.len() > 2048 || value.chars().any(|c| c.is_control() || c.is_whitespace()) {
        return Err(Error::InvalidConfig);
    }
    let (_, tail) = value.split_once("://").ok_or(Error::InvalidConfig)?;
    let authority = tail
        .split(['/', '?', '#'])
        .next()
        .ok_or(Error::InvalidConfig)?;
    if authority.contains('@') || !matches!(&tail[authority.len()..], "" | "/") {
        return Err(Error::InvalidConfig);
    }
    let url = Url::parse(value).map_err(|_| Error::InvalidConfig)?;
    if value.trim() != value
        || !url.username().is_empty()
        || url.password().is_some()
        || url.query().is_some()
        || url.fragment().is_some()
        || url.path() != "/"
        || url.host().is_none()
    {
        return Err(Error::InvalidConfig);
    }
    let host = if let Some(bracketed) = authority.strip_prefix('[') {
        bracketed.split(']').next().ok_or(Error::InvalidConfig)?
    } else {
        authority.split(':').next().ok_or(Error::InvalidConfig)?
    };
    let literal_loopback = host
        .parse::<std::net::IpAddr>()
        .is_ok_and(|ip| ip.is_loopback());
    if url.scheme() != "https" && !(url.scheme() == "http" && loopback && literal_loopback) {
        return Err(Error::InvalidConfig);
    }
    Ok(url)
}
fn validate_headers(headers: &HeaderMap, limits: Limits) -> Result<(), Error> {
    let mut size = 0usize;
    for (name, value) in headers {
        size = size
            .checked_add(name.as_str().len() + value.as_bytes().len() + 4)
            .ok_or(Error::HeadersTooLarge)?;
        if size > limits.header_bytes {
            return Err(Error::HeadersTooLarge);
        }
    }
    if headers
        .get_all(CONTENT_ENCODING)
        .iter()
        .any(|v| !v.as_bytes().eq_ignore_ascii_case(b"identity"))
    {
        return Err(Error::UnsupportedEncoding);
    }
    if headers
        .get(CONTENT_LENGTH)
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.parse::<u64>().ok())
        .is_some_and(|n| n > limits.response_bytes as u64)
    {
        return Err(Error::ResponseTooLarge);
    }
    Ok(())
}
