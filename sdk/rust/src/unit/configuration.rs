//! Isolated admission checks for externally supplied certificate bundles.

use super::*;

// Synthetic public CA only. Private keys and TLS handshakes are unnecessary here.
const PUBLIC_CA: &[u8] = br#"-----BEGIN CERTIFICATE-----
MIIDOTCCAiGgAwIBAgIUMxrK46yrjQrUnleQIjJysK10gCkwDQYJKoZIhvcNAQEL
BQAwJDEiMCAGA1UEAwwZT0FDIFJ1c3QgaXNvbGF0ZWQgdW5pdCBDQTAeFw0yNjEx
MDkwOTQxMTVaFw00NjExMDQwOTQxMTVaMCQxIjAgBgNVBAMMGU9BQyBSdXN0IGlz
b2xhdGVkIHVuaXQgQ0EwggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQDY
O+HyMlRpD0JCa+eVEg/Ukazt7mIH6mgf8vZ9qR2g3VQG6xTSyoIuF/UFZ3yQCxse
KMeqHSF6eeCEF6zWFp1bC4dpvUMeVlk6ZbFCHmDOC6VcGCQTB9sjExxOv1aueHJU
r0WM1pjzNXAXrmxTyo+soVXI11cKFNixvttomtL6mtmAtDzjlTUjOT9C1Ui4FBKx
bCp0K4EQcJ9uZ0e2rZN7hbx4QeQfgT6BpVzhwQdo/xbtFs6YC4tJ/X6XtVCSqOAs
VImBXN+83EDGI13gAkaCeZcbGUYiN1gI27gpsrCvBJwvaGiUbk6zRa42DaO0Smbb
NIXJRZq3JEyzkiToj5JtAgMBAAGjYzBhMB0GA1UdDgQWBBSMoYUJO/pEO/M+yEEy
cuiWfA81ODAfBgNVHSMEGDAWgBSMoYUJO/pEO/M+yEEycuiWfA81ODAPBgNVHRMB
Af8EBTADAQH/MA4GA1UdDwEB/wQEAwIBBjANBgkqhkiG9w0BAQsFAAOCAQEAG85h
Kc6oZeWXCnDoEuZJrJMKQ0GiLNp2PpVqiyxHzJjcUAuOFPxOKwqVtGooOBGxvE8X
UajNJNgaD1bv0ZpmBrzm1AxE3ba1cjeIdF9cnAzGCHzhyMPOdA9i4jGxKY3DjChP
hVt7msa2p12bCFr/kJOb8+0yxTlJbdYf60x2cEmsO02hgN0XGKAdeHYzQPst1oh6
awH4QclidAQVacGV3KAMHgCl7zkkppmvVlCN8tGdJuK0eOYfWjolS4dK/mTrNfbR
MY8lMMGAkShG5my29ppSIpkhzrHtYSMhWLtBiVVoyjWJWMqymfT//cBcZb5FjTji
SCloF/bMY0CYGkeKfQ==
-----END CERTIFICATE-----
"#;

/// Construction validates trust inputs without acquiring a token or sending a request.
fn configure_certificates(bundles: Vec<Vec<u8>>) -> Result<Client, Error> {
    let transport = Scripted::new(vec![]);
    let acquisitions = Arc::new(AtomicUsize::new(0));
    let observed = acquisitions.clone();
    let mut config = Config::new("https://catalog.example.com");
    config.root_certificates = bundles;
    config.token_provider = Some(Arc::new(move || {
        observed.fetch_add(1, Ordering::SeqCst);
        Box::pin(async { Ok("construction-must-not-acquire-this".into()) })
    }));
    let result = Client::with_transport(config, transport.clone());
    assert_eq!(acquisitions.load(Ordering::SeqCst), 0);
    assert!(transport.requests.lock().unwrap().is_empty());
    result
}

#[test]
fn certificate_bundle_count_accepts_valid_controls_before_refusing_excess() {
    for count in [1, 32] {
        assert!(configure_certificates(vec![PUBLIC_CA.to_vec(); count]).is_ok());
    }
    assert_eq!(
        configure_certificates(vec![PUBLIC_CA.to_vec(); 33]).unwrap_err(),
        Error::InvalidConfig
    );
}

#[test]
fn certificate_bundle_byte_budget_counts_accepted_pem_padding() {
    // Trailing whitespace preserves the same valid certificate. Thus the larger
    // neighbor isolates the byte budget rather than a PEM/DER decoding failure.
    let mut exact = PUBLIC_CA.to_vec();
    exact.resize(1 << 20, b'\n');
    assert_eq!(exact.len(), 1 << 20);
    assert!(configure_certificates(vec![exact.clone()]).is_ok());
    exact.push(b'\n');
    assert_eq!(
        configure_certificates(vec![exact]).unwrap_err(),
        Error::InvalidConfig
    );
}
