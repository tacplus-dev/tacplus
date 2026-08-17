use super::*;

use base64::Engine as _;
use std::net::SocketAddr;
use std::str::FromStr;
use std::time::Duration;
use tokio::sync::oneshot::error::TryRecvError;
use tokio_stream::wrappers::ReceiverStream;
use tonic::transport::{Endpoint, Server};
use x509_cert::der::{asn1::BitString, Decode, Encode};
use x509_cert::name::Name;
use x509_cert::serial_number::SerialNumber;
use x509_cert::spki::SubjectPublicKeyInfoOwned;
use x509_cert::time::Validity;
use x509_cert::{Certificate, TbsCertificate, Version};

type StreamMemberGrpcHandler =
    dyn crate::s2sm::internal::grpc::servers::svc::certstream::stream_member_service::StreamMemberServiceHandler;

#[derive(Debug)]
struct FixtureVerifier;

impl rustls::client::danger::ServerCertVerifier for FixtureVerifier {
    fn verify_server_cert(
        &self,
        _end_entity: &rustls::pki_types::CertificateDer<'_>,
        _intermediates: &[rustls::pki_types::CertificateDer<'_>],
        _server_name: &rustls::pki_types::ServerName<'_>,
        _ocsp_response: &[u8],
        _now: rustls::pki_types::UnixTime,
    ) -> Result<rustls::client::danger::ServerCertVerified, rustls::Error> {
        Ok(rustls::client::danger::ServerCertVerified::assertion())
    }

    fn verify_tls12_signature(
        &self,
        _message: &[u8],
        _cert: &rustls::pki_types::CertificateDer<'_>,
        _dss: &rustls::DigitallySignedStruct,
    ) -> Result<rustls::client::danger::HandshakeSignatureValid, rustls::Error> {
        Ok(rustls::client::danger::HandshakeSignatureValid::assertion())
    }

    fn verify_tls13_signature(
        &self,
        _message: &[u8],
        _cert: &rustls::pki_types::CertificateDer<'_>,
        _dss: &rustls::DigitallySignedStruct,
    ) -> Result<rustls::client::danger::HandshakeSignatureValid, rustls::Error> {
        Ok(rustls::client::danger::HandshakeSignatureValid::assertion())
    }

    fn supported_verify_schemes(&self) -> Vec<rustls::SignatureScheme> {
        vec![rustls::SignatureScheme::ED25519]
    }
}

struct UnusedHandler;

#[async_trait::async_trait]
impl Handler for UnusedHandler {
    fn stream_member_grpc_impl(&self) -> Arc<StreamMemberGrpcHandler> {
        unreachable!("the no-connection fixture never invokes service handlers")
    }

    async fn new_smb_cert(
        &self,
        _member_chain: &x509_pb::X509CertChain,
        _private_key_der: &[u8],
        _ca: &x509_pb::X509Cert,
    ) -> Result<(), Box<dyn std::error::Error>> {
        unreachable!("the no-connection fixture never delivers SMB credentials")
    }

    async fn shutdown(&self) {}
}

#[derive(Default)]
struct RecordingHandler {
    tls_keys: std::sync::Mutex<Vec<Vec<u8>>>,
    smb_keys: std::sync::Mutex<Vec<Vec<u8>>>,
    fail_tls_at: Option<usize>,
    fail_smb_at: Option<usize>,
}

impl RecordingHandler {
    fn fail_tls_at(call: usize) -> Self {
        Self {
            fail_tls_at: Some(call),
            ..Default::default()
        }
    }

    fn fail_smb_at(call: usize) -> Self {
        Self {
            fail_smb_at: Some(call),
            ..Default::default()
        }
    }
}

#[async_trait::async_trait]
impl Handler for RecordingHandler {
    fn stream_member_grpc_impl(&self) -> Arc<StreamMemberGrpcHandler> {
        unreachable!("the cert-stream fixture never invokes service handlers")
    }

    async fn new_tls_cert(
        &self,
        _cert_chain: &x509_pb::X509CertChain,
        private_key_der: &[u8],
    ) -> Result<(), Box<dyn std::error::Error>> {
        let mut keys = self.tls_keys.lock().expect("tls key recorder lock");
        keys.push(private_key_der.to_vec());
        if self.fail_tls_at == Some(keys.len()) {
            return Err(std::io::Error::new(
                std::io::ErrorKind::Other,
                format!("reject TLS cert delivery {}", keys.len()),
            )
            .into());
        }
        Ok(())
    }

    async fn new_smb_cert(
        &self,
        _member_chain: &x509_pb::X509CertChain,
        private_key_der: &[u8],
        _ca: &x509_pb::X509Cert,
    ) -> Result<(), Box<dyn std::error::Error>> {
        let mut keys = self.smb_keys.lock().expect("smb key recorder lock");
        keys.push(private_key_der.to_vec());
        if self.fail_smb_at == Some(keys.len()) {
            return Err(std::io::Error::new(
                std::io::ErrorKind::Other,
                format!("reject SMB cert delivery {}", keys.len()),
            )
            .into());
        }
        Ok(())
    }

    async fn shutdown(&self) {}
}

fn empty_authority_connection() -> Arc<ServiceConnection> {
    Arc::new(ServiceConnection::new_dynamic(
        "cert-stream-authority",
        Arc::new(FixtureVerifier),
    ))
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum AuthorityMode {
    FirstSuccess,
    RenewalSuccess,
    CleanBeforeFirst,
    ServerErrorBefore,
    ServerErrorAfter,
    ReorderedRenewal,
}

#[derive(Clone)]
struct RustCertAuthority {
    mode: AuthorityMode,
}

type TlsAuthorityStream = ReceiverStream<Result<tlscert_pb::TlsCertServerMsg, tonic::Status>>;
type SmbAuthorityStream = ReceiverStream<Result<smbcert_pb::SmbCertServerMsg, tonic::Status>>;

#[tonic::async_trait]
impl crate::types::sys::tlscert::tls_cert_service_server::TlsCertService for RustCertAuthority {
    type ManageCertStream = TlsAuthorityStream;

    async fn manage_cert(
        &self,
        request: tonic::Request<tonic::Streaming<tlscert_pb::TlsCertClientMsg>>,
    ) -> Result<tonic::Response<Self::ManageCertStream>, tonic::Status> {
        let mut inbound = request.into_inner();
        let (tx, rx) = tokio::sync::mpsc::channel(4);
        let mode = self.mode;

        tokio::spawn(async move {
            let first_key = match recv_tls_public_key(&mut inbound).await {
                Ok(key) => key,
                Err(status) => {
                    let _ = tx.send(Err(status)).await;
                    return;
                }
            };
            if mode == AuthorityMode::CleanBeforeFirst {
                return;
            }
            if mode == AuthorityMode::ServerErrorBefore {
                let _ = send_tls_error(&tx, "fixture before first delivery").await;
                return;
            }
            if send_tls_cert(&tx, &first_key, "rust-tls-initial")
                .await
                .is_err()
            {
                return;
            }
            if mode == AuthorityMode::ServerErrorAfter {
                let _ = send_tls_error(&tx, "fixture after first delivery").await;
                return;
            }
            if mode != AuthorityMode::RenewalSuccess && mode != AuthorityMode::ReorderedRenewal {
                return;
            }
            if send_tls_renew(&tx, "fixture renewal").await.is_err() {
                return;
            }
            let renewal_key = match recv_tls_public_key(&mut inbound).await {
                Ok(key) => key,
                Err(status) => {
                    let _ = tx.send(Err(status)).await;
                    return;
                }
            };
            if mode == AuthorityMode::ReorderedRenewal {
                if send_tls_cert(&tx, &first_key, "rust-tls-stale-renewal")
                    .await
                    .is_err()
                {
                    return;
                }
                if send_tls_renew(&tx, "fixture renewal retry").await.is_err() {
                    return;
                }
                let renewal_key = match recv_tls_public_key(&mut inbound).await {
                    Ok(key) => key,
                    Err(status) => {
                        let _ = tx.send(Err(status)).await;
                        return;
                    }
                };
                let _ = send_tls_cert(&tx, &renewal_key, "rust-tls-renewal").await;
                return;
            }
            let _ = send_tls_cert(&tx, &renewal_key, "rust-tls-renewal").await;
        });

        Ok(tonic::Response::new(ReceiverStream::new(rx)))
    }
}

#[tonic::async_trait]
impl crate::types::sys::smbcert::smb_cert_service_server::SmbCertService for RustCertAuthority {
    type ManageSmbCertStream = SmbAuthorityStream;

    async fn manage_smb_cert(
        &self,
        request: tonic::Request<tonic::Streaming<smbcert_pb::SmbCertClientMsg>>,
    ) -> Result<tonic::Response<Self::ManageSmbCertStream>, tonic::Status> {
        let mut inbound = request.into_inner();
        let (tx, rx) = tokio::sync::mpsc::channel(4);
        let mode = self.mode;

        tokio::spawn(async move {
            let first_key = match recv_smb_public_key(&mut inbound).await {
                Ok(key) => key,
                Err(status) => {
                    let _ = tx.send(Err(status)).await;
                    return;
                }
            };
            if mode == AuthorityMode::CleanBeforeFirst {
                return;
            }
            if mode == AuthorityMode::ServerErrorBefore {
                let _ = send_smb_error(&tx, "fixture before first delivery").await;
                return;
            }
            if send_smb_cert(&tx, &first_key, "rust-smb-initial")
                .await
                .is_err()
            {
                return;
            }
            if mode == AuthorityMode::ServerErrorAfter {
                let _ = send_smb_error(&tx, "fixture after first delivery").await;
                return;
            }
            if mode != AuthorityMode::RenewalSuccess && mode != AuthorityMode::ReorderedRenewal {
                return;
            }
            if send_smb_renew(&tx, "fixture renewal").await.is_err() {
                return;
            }
            let renewal_key = match recv_smb_public_key(&mut inbound).await {
                Ok(key) => key,
                Err(status) => {
                    let _ = tx.send(Err(status)).await;
                    return;
                }
            };
            if mode == AuthorityMode::ReorderedRenewal {
                if send_smb_cert(&tx, &first_key, "rust-smb-stale-renewal")
                    .await
                    .is_err()
                {
                    return;
                }
                if send_smb_renew(&tx, "fixture renewal retry").await.is_err() {
                    return;
                }
                let renewal_key = match recv_smb_public_key(&mut inbound).await {
                    Ok(key) => key,
                    Err(status) => {
                        let _ = tx.send(Err(status)).await;
                        return;
                    }
                };
                let _ = send_smb_cert(&tx, &renewal_key, "rust-smb-renewal").await;
                return;
            }
            let _ = send_smb_cert(&tx, &renewal_key, "rust-smb-renewal").await;
        });

        Ok(tonic::Response::new(ReceiverStream::new(rx)))
    }
}

async fn start_rust_authority(
    mode: AuthorityMode,
) -> (Arc<ServiceConnection>, tokio::task::JoinHandle<()>) {
    let addr = reserve_local_addr();
    let authority = RustCertAuthority { mode };
    let server = tokio::spawn(async move {
        Server::builder()
            .add_service(
                crate::types::sys::tlscert::tls_cert_service_server::TlsCertServiceServer::new(
                    authority.clone(),
                ),
            )
            .add_service(
                crate::types::sys::smbcert::smb_cert_service_server::SmbCertServiceServer::new(
                    authority,
                ),
            )
            .serve(addr)
            .await
            .expect("serve rust cert authority");
    });

    let channel = connect_plain_channel(addr).await;
    let conn = Arc::new(ServiceConnection::from_channel(
        "cert-stream-authority",
        channel,
        Arc::new(FixtureVerifier),
    ));
    (conn, server)
}

fn reserve_local_addr() -> SocketAddr {
    let listener = std::net::TcpListener::bind("127.0.0.1:0").expect("bind rust authority");
    let addr = listener.local_addr().expect("rust authority addr");
    drop(listener);
    addr
}

async fn connect_plain_channel(addr: SocketAddr) -> Channel {
    let endpoint = format!("http://{addr}");
    for _ in 0..50 {
        match Endpoint::from_shared(endpoint.clone())
            .expect("valid rust authority endpoint")
            .connect()
            .await
        {
            Ok(channel) => return channel,
            Err(_) => tokio::time::sleep(Duration::from_millis(10)).await,
        }
    }
    panic!("rust authority did not accept connections at {endpoint}");
}

async fn recv_tls_public_key(
    inbound: &mut tonic::Streaming<tlscert_pb::TlsCertClientMsg>,
) -> Result<key_pb::PublicKey, tonic::Status> {
    while let Some(msg) = inbound.message().await? {
        if let Some(tlscert_pb::tls_cert_client_msg::Msg::Csr(csr)) = msg.msg {
            if let Some(public_key) = csr.public_key {
                return Ok(public_key);
            }
        }
    }
    Err(tonic::Status::invalid_argument("missing TLS CSR"))
}

async fn recv_smb_public_key(
    inbound: &mut tonic::Streaming<smbcert_pb::SmbCertClientMsg>,
) -> Result<key_pb::PublicKey, tonic::Status> {
    while let Some(msg) = inbound.message().await? {
        if let Some(smbcert_pb::smb_cert_client_msg::Msg::Csr(csr)) = msg.msg {
            if let Some(public_key) = csr.public_key {
                return Ok(public_key);
            }
        }
    }
    Err(tonic::Status::invalid_argument("missing SMB CSR"))
}

async fn send_tls_cert(
    tx: &tokio::sync::mpsc::Sender<Result<tlscert_pb::TlsCertServerMsg, tonic::Status>>,
    public_key: &key_pb::PublicKey,
    id: &str,
) -> Result<
    (),
    tokio::sync::mpsc::error::SendError<Result<tlscert_pb::TlsCertServerMsg, tonic::Status>>,
> {
    tx.send(Ok(tlscert_pb::TlsCertServerMsg {
        msg: Some(tlscert_pb::tls_cert_server_msg::Msg::Cert(
            tlscert_pb::TlsCertDelivery {
                chain: Some(cert_chain_for_public_key(public_key, id)),
            },
        )),
    }))
    .await
}

async fn send_tls_renew(
    tx: &tokio::sync::mpsc::Sender<Result<tlscert_pb::TlsCertServerMsg, tonic::Status>>,
    reason: &str,
) -> Result<
    (),
    tokio::sync::mpsc::error::SendError<Result<tlscert_pb::TlsCertServerMsg, tonic::Status>>,
> {
    tx.send(Ok(tlscert_pb::TlsCertServerMsg {
        msg: Some(tlscert_pb::tls_cert_server_msg::Msg::Renew(
            tlscert_pb::TlsCertRenew {
                reason: reason.to_string(),
            },
        )),
    }))
    .await
}

async fn send_tls_error(
    tx: &tokio::sync::mpsc::Sender<Result<tlscert_pb::TlsCertServerMsg, tonic::Status>>,
    error: &str,
) -> Result<
    (),
    tokio::sync::mpsc::error::SendError<Result<tlscert_pb::TlsCertServerMsg, tonic::Status>>,
> {
    tx.send(Ok(tlscert_pb::TlsCertServerMsg {
        msg: Some(tlscert_pb::tls_cert_server_msg::Msg::Error(
            error.to_string(),
        )),
    }))
    .await
}

async fn send_smb_cert(
    tx: &tokio::sync::mpsc::Sender<Result<smbcert_pb::SmbCertServerMsg, tonic::Status>>,
    public_key: &key_pb::PublicKey,
    id: &str,
) -> Result<
    (),
    tokio::sync::mpsc::error::SendError<Result<smbcert_pb::SmbCertServerMsg, tonic::Status>>,
> {
    tx.send(Ok(smbcert_pb::SmbCertServerMsg {
        msg: Some(smbcert_pb::smb_cert_server_msg::Msg::Cert(
            smbcert_pb::SmbCertDelivery {
                member_chain: Some(cert_chain_for_public_key(public_key, id)),
                ca: Some(proto_cert("rust-smb-ca", &[1, 2, 3])),
            },
        )),
    }))
    .await
}

async fn send_smb_renew(
    tx: &tokio::sync::mpsc::Sender<Result<smbcert_pb::SmbCertServerMsg, tonic::Status>>,
    reason: &str,
) -> Result<
    (),
    tokio::sync::mpsc::error::SendError<Result<smbcert_pb::SmbCertServerMsg, tonic::Status>>,
> {
    tx.send(Ok(smbcert_pb::SmbCertServerMsg {
        msg: Some(smbcert_pb::smb_cert_server_msg::Msg::Renew(
            smbcert_pb::SmbCertRenew {
                reason: reason.to_string(),
            },
        )),
    }))
    .await
}

async fn send_smb_error(
    tx: &tokio::sync::mpsc::Sender<Result<smbcert_pb::SmbCertServerMsg, tonic::Status>>,
    error: &str,
) -> Result<
    (),
    tokio::sync::mpsc::error::SendError<Result<smbcert_pb::SmbCertServerMsg, tonic::Status>>,
> {
    tx.send(Ok(smbcert_pb::SmbCertServerMsg {
        msg: Some(smbcert_pb::smb_cert_server_msg::Msg::Error(
            error.to_string(),
        )),
    }))
    .await
}

fn cert_chain_for_public_key(public_key: &key_pb::PublicKey, id: &str) -> x509_pb::X509CertChain {
    let cert_der = minimal_cert_der(public_key, id).expect("build fixture cert");
    x509_pb::X509CertChain {
        chain: vec![proto_cert(id, &cert_der)],
        key: None,
        id: id.to_string(),
    }
}

fn proto_cert(id: &str, der: &[u8]) -> x509_pb::X509Cert {
    x509_pb::X509Cert {
        id: id.to_string(),
        base64: base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(der),
    }
}

fn minimal_cert_der(
    public_key: &key_pb::PublicKey,
    id: &str,
) -> Result<Vec<u8>, Box<dyn std::error::Error>> {
    let public_key_der = public_key_der(public_key)?;
    let subject_public_key_info = SubjectPublicKeyInfoOwned::from_der(&public_key_der)?;
    let signature_algorithm = subject_public_key_info.algorithm.clone();
    let name = Name::from_str(&format!("CN={id}"))?;
    let cert = Certificate {
        tbs_certificate: TbsCertificate {
            version: Version::V3,
            serial_number: SerialNumber::new(&[1])?,
            signature: signature_algorithm.clone(),
            issuer: name.clone(),
            validity: Validity::from_now(Duration::from_secs(3600))?,
            subject: name,
            subject_public_key_info,
            issuer_unique_id: None,
            subject_unique_id: None,
            extensions: None,
        },
        signature_algorithm,
        signature: BitString::from_bytes(&[1, 2, 3])?,
    };
    Ok(cert.to_der()?)
}

fn public_key_der(public_key: &key_pb::PublicKey) -> Result<Vec<u8>, Box<dyn std::error::Error>> {
    let raw = base64::engine::general_purpose::URL_SAFE_NO_PAD
        .decode(&public_key.base64)
        .or_else(|_| base64::engine::general_purpose::URL_SAFE.decode(&public_key.base64))
        .or_else(|_| base64::engine::general_purpose::STANDARD_NO_PAD.decode(&public_key.base64))
        .or_else(|_| base64::engine::general_purpose::STANDARD.decode(&public_key.base64))?;
    let pem = std::str::from_utf8(&raw)?;
    let body: String = pem
        .lines()
        .filter(|line| !line.starts_with("-----"))
        .collect();
    Ok(base64::engine::general_purpose::STANDARD.decode(body.as_bytes())?)
}

fn fixture_token_provider() -> TokenProvider {
    Arc::new(|| Box::pin(async { Ok("fixture-token".to_string()) }))
}

fn spawn_tls_loop(
    ready_tx: Option<tokio::sync::oneshot::Sender<()>>,
) -> tokio::task::JoinHandle<()> {
    let handler_slot = Arc::new(tokio::sync::RwLock::new(None));
    let cert_der_store = Arc::new(tokio::sync::RwLock::new(None));
    let key_der_store = Arc::new(tokio::sync::RwLock::new(None));
    let cert_proto_store = Arc::new(tokio::sync::RwLock::new(None));
    let cert_pem_store = Arc::new(tokio::sync::RwLock::new(None));
    let key_pem_store = Arc::new(tokio::sync::RwLock::new(None));
    let server_certified_key_store = Arc::new(std::sync::RwLock::new(None));
    let tls_degrade = Arc::new(std::sync::Mutex::new(fixture_tls_degrade_seam()));

    tokio::spawn(tls_cert_stream_loop(
        empty_authority_connection(),
        "StreamMemberService".to_string(),
        "instance-1".to_string(),
        handler_slot,
        fixture_token_provider(),
        ready_tx,
        cert_der_store,
        key_der_store,
        cert_proto_store,
        cert_pem_store,
        key_pem_store,
        server_certified_key_store,
        tls_degrade,
    ))
}

fn spawn_smb_loop(
    ready_tx: Option<tokio::sync::oneshot::Sender<Result<(), String>>>,
) -> tokio::task::JoinHandle<()> {
    let handler: Arc<dyn Handler> = Arc::new(UnusedHandler);

    tokio::spawn(smb_cert_stream_loop(
        empty_authority_connection(),
        "StreamMemberService".to_string(),
        "instance-1".to_string(),
        handler,
        fixture_token_provider(),
        ready_tx,
    ))
}

async fn assert_task_aborts(handle: tokio::task::JoinHandle<()>, label: &str) {
    handle.abort();
    let join = tokio::time::timeout(std::time::Duration::from_millis(250), handle)
        .await
        .unwrap_or_else(|_| panic!("{label} did not exit after abort"));
    let err = join.expect_err("aborted cert-stream loop should return a JoinError");
    assert!(
        err.is_cancelled(),
        "{label} returned non-cancel JoinError: {err}"
    );
}

struct TlsStores {
    cert_der_store: Arc<tokio::sync::RwLock<Option<Vec<Vec<u8>>>>>,
    key_der_store: Arc<tokio::sync::RwLock<Option<Vec<u8>>>>,
    cert_proto_store: Arc<tokio::sync::RwLock<Option<x509_pb::X509CertChain>>>,
    cert_pem_store: Arc<tokio::sync::RwLock<Option<String>>>,
    key_pem_store: Arc<tokio::sync::RwLock<Option<String>>>,
    server_certified_key_store: Arc<std::sync::RwLock<Option<Arc<CertifiedKey>>>>,
    tls_degrade: Arc<std::sync::Mutex<TlsDegradeSeam>>,
}

impl TlsStores {
    fn new() -> Self {
        Self {
            cert_der_store: Arc::new(tokio::sync::RwLock::new(None)),
            key_der_store: Arc::new(tokio::sync::RwLock::new(None)),
            cert_proto_store: Arc::new(tokio::sync::RwLock::new(None)),
            cert_pem_store: Arc::new(tokio::sync::RwLock::new(None)),
            key_pem_store: Arc::new(tokio::sync::RwLock::new(None)),
            server_certified_key_store: Arc::new(std::sync::RwLock::new(None)),
            tls_degrade: Arc::new(std::sync::Mutex::new(fixture_tls_degrade_seam())),
        }
    }
}

fn fixture_tls_degrade_seam() -> TlsDegradeSeam {
    TlsDegradeSeam {
        fns: Vec::new(),
        seen: Vec::new(),
    }
}

async fn run_tls_authority_fixture(
    mode: AuthorityMode,
    handler: Arc<RecordingHandler>,
) -> (
    Result<bool, Box<dyn std::error::Error>>,
    Option<tokio::sync::oneshot::Sender<()>>,
    tokio::sync::oneshot::Receiver<()>,
    TlsStores,
    tokio::task::JoinHandle<()>,
) {
    let (conn, server) = start_rust_authority(mode).await;
    let handler_dyn: Arc<dyn Handler> = handler;
    let handler_slot = Arc::new(tokio::sync::RwLock::new(Some(handler_dyn)));
    let stores = TlsStores::new();
    let (tx, rx) = tokio::sync::oneshot::channel();
    let mut ready = Some(tx);

    let result = run_tls_cert_stream(
        conn,
        "StreamMemberService",
        "instance-1",
        &handler_slot,
        fixture_token_provider(),
        &mut ready,
        &stores.cert_der_store,
        &stores.key_der_store,
        &stores.cert_proto_store,
        &stores.cert_pem_store,
        &stores.key_pem_store,
        stores.server_certified_key_store.clone(),
        &stores.tls_degrade,
    )
    .await;

    (result, ready, rx, stores, server)
}

async fn run_smb_authority_fixture(
    mode: AuthorityMode,
    handler: Arc<RecordingHandler>,
) -> (
    Result<bool, Box<dyn std::error::Error>>,
    Option<tokio::sync::oneshot::Sender<Result<(), String>>>,
    tokio::sync::oneshot::Receiver<Result<(), String>>,
    tokio::task::JoinHandle<()>,
) {
    let (conn, server) = start_rust_authority(mode).await;
    let handler_dyn: Arc<dyn Handler> = handler;
    let (tx, rx) = tokio::sync::oneshot::channel();
    let mut ready = Some(tx);

    let result = run_smb_cert_stream(
        conn,
        "StreamMemberService",
        "instance-1",
        &handler_dyn,
        fixture_token_provider(),
        &mut ready,
    )
    .await;

    (result, ready, rx, server)
}

fn assert_err_contains<T>(result: &Result<T, Box<dyn std::error::Error>>, expected: &str) {
    match result {
        Err(err) => assert!(
            err.to_string().contains(expected),
            "expected error containing {expected:?}, got {err}"
        ),
        Ok(_) => panic!("expected error containing {expected:?}, got success"),
    }
}

#[tokio::test]
async fn rust_tls_cert_stream_reports_missing_authority_without_ready_signal() {
    let handler_slot = Arc::new(tokio::sync::RwLock::new(None));
    let cert_der_store = Arc::new(tokio::sync::RwLock::new(None));
    let key_der_store = Arc::new(tokio::sync::RwLock::new(None));
    let cert_proto_store = Arc::new(tokio::sync::RwLock::new(None));
    let cert_pem_store = Arc::new(tokio::sync::RwLock::new(None));
    let key_pem_store = Arc::new(tokio::sync::RwLock::new(None));
    let server_certified_key_store = Arc::new(std::sync::RwLock::new(None));
    let tls_degrade = std::sync::Mutex::new(fixture_tls_degrade_seam());
    let (tx, mut rx) = tokio::sync::oneshot::channel();
    let mut ready = Some(tx);

    let err = run_tls_cert_stream(
        empty_authority_connection(),
        "StreamMemberService",
        "instance-1",
        &handler_slot,
        fixture_token_provider(),
        &mut ready,
        &cert_der_store,
        &key_der_store,
        &cert_proto_store,
        &cert_pem_store,
        &key_pem_store,
        server_certified_key_store,
        &tls_degrade,
    )
    .await
    .expect_err("missing authority connection must be retryable");

    assert!(err
        .to_string()
        .contains("no active connection for ManageCert"));
    assert!(
        ready.is_some(),
        "first-delivery signal should remain pending"
    );
    assert!(matches!(rx.try_recv(), Err(TryRecvError::Empty)));
}

#[tokio::test]
async fn rust_tls_first_delivery_and_renewal_use_distinct_local_keys() {
    let (conn, server) = start_rust_authority(AuthorityMode::RenewalSuccess).await;
    let handler = Arc::new(RecordingHandler::default());
    let handler_dyn: Arc<dyn Handler> = handler.clone();
    let handler_slot = Arc::new(tokio::sync::RwLock::new(Some(handler_dyn)));
    let cert_der_store = Arc::new(tokio::sync::RwLock::new(None));
    let key_der_store = Arc::new(tokio::sync::RwLock::new(None));
    let cert_proto_store = Arc::new(tokio::sync::RwLock::new(None));
    let cert_pem_store = Arc::new(tokio::sync::RwLock::new(None));
    let key_pem_store = Arc::new(tokio::sync::RwLock::new(None));
    let server_certified_key_store = Arc::new(std::sync::RwLock::new(None));
    let tls_degrade = std::sync::Mutex::new(fixture_tls_degrade_seam());
    let (tx, rx) = tokio::sync::oneshot::channel();
    let mut ready = Some(tx);

    let delivered = run_tls_cert_stream(
        conn,
        "StreamMemberService",
        "instance-1",
        &handler_slot,
        fixture_token_provider(),
        &mut ready,
        &cert_der_store,
        &key_der_store,
        &cert_proto_store,
        &cert_pem_store,
        &key_pem_store,
        server_certified_key_store,
        &tls_degrade,
    )
    .await
    .expect("TLS authority fixture should complete after renewal");

    assert!(delivered, "TLS fixture should deliver the first cert");
    assert!(ready.is_none(), "first-delivery signal should be consumed");
    rx.await.expect("TLS ready signal should be published");
    assert!(
        cert_der_store.read().await.is_some(),
        "TLS cert store should be populated"
    );
    assert!(
        key_der_store.read().await.is_some(),
        "TLS key store should be populated"
    );
    let keys = handler.tls_keys.lock().expect("tls key recorder lock");
    assert_eq!(keys.len(), 2, "expected initial + renewal TLS deliveries");
    assert_ne!(keys[0], keys[1], "TLS renewal must use a fresh local key");
    server.abort();
}

#[tokio::test]
async fn rust_tls_clean_end_before_first_delivery_is_startup_failure() {
    let handler = Arc::new(RecordingHandler::default());
    let (result, ready, mut rx, _stores, server) =
        run_tls_authority_fixture(AuthorityMode::CleanBeforeFirst, handler.clone()).await;

    assert!(
        !result.expect("TLS clean pre-first end should be retryable"),
        "clean pre-first end must not report delivery"
    );
    assert!(
        ready.is_some(),
        "first-delivery signal should remain pending"
    );
    assert!(matches!(rx.try_recv(), Err(TryRecvError::Empty)));
    assert_eq!(
        handler
            .tls_keys
            .lock()
            .expect("tls key recorder lock")
            .len(),
        0,
        "handler should not be called before first delivery"
    );
    server.abort();
}

#[tokio::test]
async fn rust_tls_server_error_before_first_delivery_is_startup_failure() {
    let handler = Arc::new(RecordingHandler::default());
    let (result, ready, mut rx, _stores, server) =
        run_tls_authority_fixture(AuthorityMode::ServerErrorBefore, handler.clone()).await;

    assert_err_contains(&result, "server error: fixture before first delivery");
    assert!(
        ready.is_some(),
        "first-delivery signal should remain pending"
    );
    assert!(matches!(rx.try_recv(), Err(TryRecvError::Empty)));
    assert_eq!(
        handler
            .tls_keys
            .lock()
            .expect("tls key recorder lock")
            .len(),
        0,
        "handler should not be called before first delivery"
    );
    server.abort();
}

#[tokio::test]
async fn rust_tls_server_error_after_first_delivery_is_reconnectable() {
    let handler = Arc::new(RecordingHandler::default());
    let (result, ready, rx, _stores, server) =
        run_tls_authority_fixture(AuthorityMode::ServerErrorAfter, handler.clone()).await;

    assert_err_contains(&result, "server error: fixture after first delivery");
    assert!(ready.is_none(), "first-delivery signal should be consumed");
    rx.await.expect("TLS ready signal should be published");
    assert_eq!(
        handler
            .tls_keys
            .lock()
            .expect("tls key recorder lock")
            .len(),
        1,
        "expected exactly one installed TLS cert before reconnectable error"
    );
    server.abort();
}

#[tokio::test]
async fn rust_tls_first_handler_failure_keeps_startup_pending() {
    let handler = Arc::new(RecordingHandler::fail_tls_at(1));
    let (result, ready, mut rx, stores, server) =
        run_tls_authority_fixture(AuthorityMode::FirstSuccess, handler.clone()).await;

    assert!(
        !result.expect("TLS first-handler rejection is a fatal startup error (no retry)"),
        "failed first handler delivery must not report readiness"
    );
    assert!(
        ready.is_some(),
        "first-delivery signal should remain pending after handler rejection"
    );
    assert!(matches!(rx.try_recv(), Err(TryRecvError::Empty)));
    assert!(
        stores.cert_der_store.read().await.is_none(),
        "rejected first TLS cert should not populate the cert store"
    );
    assert_eq!(
        handler
            .tls_keys
            .lock()
            .expect("tls key recorder lock")
            .len(),
        1,
        "first TLS cert should be attempted once"
    );
    server.abort();
}

#[tokio::test]
async fn rust_tls_renewal_handler_failure_keeps_previous_credential() {
    let handler = Arc::new(RecordingHandler::fail_tls_at(2));
    let (result, ready, rx, stores, server) =
        run_tls_authority_fixture(AuthorityMode::RenewalSuccess, handler.clone()).await;

    assert!(
        result.expect("TLS renewal handler failure should not fail the stream"),
        "first delivery should still be reported"
    );
    assert!(ready.is_none(), "first-delivery signal should be consumed");
    rx.await.expect("TLS ready signal should be published");
    let keys = handler
        .tls_keys
        .lock()
        .expect("tls key recorder lock")
        .clone();
    assert_eq!(keys.len(), 2, "expected renewal handler attempt");
    let stored_key = stores
        .key_der_store
        .read()
        .await
        .clone()
        .expect("TLS key store should keep the accepted first key");
    assert_eq!(
        stored_key, keys[0],
        "TLS renewal handler failure should keep the previous credential"
    );
    server.abort();
}

#[tokio::test]
async fn rust_tls_stale_renewal_cert_is_ignored_until_fresh_csr_is_signed() {
    let handler = Arc::new(RecordingHandler::default());
    let (result, ready, rx, stores, server) =
        run_tls_authority_fixture(AuthorityMode::ReorderedRenewal, handler.clone()).await;

    assert!(
        result.expect("TLS reordered-renewal fixture should complete"),
        "first delivery should be reported"
    );
    assert!(ready.is_none(), "first-delivery signal should be consumed");
    rx.await.expect("TLS ready signal should be published");
    let keys = handler
        .tls_keys
        .lock()
        .expect("tls key recorder lock")
        .clone();
    assert_eq!(
        keys.len(),
        2,
        "expected initial + valid renewal TLS deliveries"
    );
    assert_ne!(
        keys[0], keys[1],
        "fresh TLS renewal should install a new key"
    );
    let stored_key = stores
        .key_der_store
        .read()
        .await
        .clone()
        .expect("TLS key store should be populated");
    assert_eq!(
        stored_key, keys[1],
        "stale TLS renewal should be ignored before the fresh CSR is signed"
    );
    server.abort();
}

#[tokio::test]
async fn rust_tls_startup_backoff_task_exits_when_aborted() {
    let (tx, mut rx) = tokio::sync::oneshot::channel();
    let handle = spawn_tls_loop(Some(tx));

    tokio::time::sleep(std::time::Duration::from_millis(25)).await;
    assert!(matches!(rx.try_recv(), Err(TryRecvError::Empty)));
    assert_task_aborts(handle, "TLS startup backoff").await;
}

#[tokio::test]
async fn rust_tls_renewal_backoff_task_exits_when_aborted() {
    let handle = spawn_tls_loop(None);

    tokio::time::sleep(std::time::Duration::from_millis(25)).await;
    assert_task_aborts(handle, "TLS renewal backoff").await;
}

#[tokio::test]
async fn rust_smb_cert_stream_reports_missing_authority_without_ready_signal() {
    let handler: Arc<dyn Handler> = Arc::new(UnusedHandler);
    let (tx, mut rx) = tokio::sync::oneshot::channel();
    let mut ready = Some(tx);

    let err = run_smb_cert_stream(
        empty_authority_connection(),
        "StreamMemberService",
        "instance-1",
        &handler,
        fixture_token_provider(),
        &mut ready,
    )
    .await
    .expect_err("missing SMB authority connection must be retryable");

    assert!(err
        .to_string()
        .contains("no active connection for ManageSmbCert"));
    assert!(
        ready.is_some(),
        "first-delivery signal should remain pending"
    );
    assert!(matches!(rx.try_recv(), Err(TryRecvError::Empty)));
}

#[tokio::test]
async fn rust_smb_first_delivery_and_renewal_use_distinct_local_keys() {
    let (conn, server) = start_rust_authority(AuthorityMode::RenewalSuccess).await;
    let handler = Arc::new(RecordingHandler::default());
    let handler_dyn: Arc<dyn Handler> = handler.clone();
    let (tx, rx) = tokio::sync::oneshot::channel();
    let mut ready = Some(tx);

    let fatal = run_smb_cert_stream(
        conn,
        "StreamMemberService",
        "instance-1",
        &handler_dyn,
        fixture_token_provider(),
        &mut ready,
    )
    .await
    .expect("SMB authority fixture should complete after renewal");

    assert!(!fatal, "SMB renewal success should not be fatal");
    assert!(ready.is_none(), "first-delivery signal should be consumed");
    assert_eq!(
        rx.await.expect("SMB ready signal should be published"),
        Ok(())
    );
    let keys = handler.smb_keys.lock().expect("smb key recorder lock");
    assert_eq!(keys.len(), 2, "expected initial + renewal SMB deliveries");
    assert_ne!(keys[0], keys[1], "SMB renewal must use a fresh local key");
    server.abort();
}

#[tokio::test]
async fn rust_smb_clean_end_before_first_delivery_is_startup_failure() {
    let handler = Arc::new(RecordingHandler::default());
    let (result, ready, mut rx, server) =
        run_smb_authority_fixture(AuthorityMode::CleanBeforeFirst, handler.clone()).await;

    assert!(
        !result.expect("SMB clean pre-first end should be retryable"),
        "clean pre-first end must not report fatal handler failure"
    );
    assert!(
        ready.is_some(),
        "first-delivery signal should remain pending"
    );
    assert!(matches!(rx.try_recv(), Err(TryRecvError::Empty)));
    assert_eq!(
        handler
            .smb_keys
            .lock()
            .expect("smb key recorder lock")
            .len(),
        0,
        "handler should not be called before first delivery"
    );
    server.abort();
}

#[tokio::test]
async fn rust_smb_server_error_before_first_delivery_is_startup_failure() {
    let handler = Arc::new(RecordingHandler::default());
    let (result, ready, mut rx, server) =
        run_smb_authority_fixture(AuthorityMode::ServerErrorBefore, handler.clone()).await;

    assert_err_contains(&result, "server error: fixture before first delivery");
    assert!(
        ready.is_some(),
        "first-delivery signal should remain pending"
    );
    assert!(matches!(rx.try_recv(), Err(TryRecvError::Empty)));
    assert_eq!(
        handler
            .smb_keys
            .lock()
            .expect("smb key recorder lock")
            .len(),
        0,
        "handler should not be called before first delivery"
    );
    server.abort();
}

#[tokio::test]
async fn rust_smb_server_error_after_first_delivery_is_reconnectable() {
    let handler = Arc::new(RecordingHandler::default());
    let (result, ready, rx, server) =
        run_smb_authority_fixture(AuthorityMode::ServerErrorAfter, handler.clone()).await;

    assert_err_contains(&result, "server error: fixture after first delivery");
    assert!(ready.is_none(), "first-delivery signal should be consumed");
    assert_eq!(
        rx.await.expect("SMB ready signal should be published"),
        Ok(())
    );
    assert_eq!(
        handler
            .smb_keys
            .lock()
            .expect("smb key recorder lock")
            .len(),
        1,
        "expected exactly one installed SMB cert before reconnectable error"
    );
    server.abort();
}

#[tokio::test]
async fn rust_smb_first_handler_failure_is_fatal() {
    let handler = Arc::new(RecordingHandler::fail_smb_at(1));
    let (result, ready, rx, server) =
        run_smb_authority_fixture(AuthorityMode::FirstSuccess, handler.clone()).await;

    assert!(
        result.expect("SMB first handler rejection should return fatal outcome"),
        "first handler failure should be fatal"
    );
    assert!(ready.is_none(), "first-delivery signal should be consumed");
    let ready_err = rx
        .await
        .expect("SMB ready signal should be sent")
        .expect_err("SMB ready signal should carry handler error");
    assert!(
        ready_err.contains("reject SMB cert delivery 1"),
        "unexpected SMB handler error: {ready_err}"
    );
    assert_eq!(
        handler
            .smb_keys
            .lock()
            .expect("smb key recorder lock")
            .len(),
        1,
        "first SMB cert should be attempted once"
    );
    server.abort();
}

#[tokio::test]
async fn rust_smb_renewal_handler_failure_keeps_loop_nonfatal() {
    let handler = Arc::new(RecordingHandler::fail_smb_at(2));
    let (result, ready, rx, server) =
        run_smb_authority_fixture(AuthorityMode::RenewalSuccess, handler.clone()).await;

    assert!(
        !result.expect("SMB renewal handler failure should not be fatal"),
        "renewal handler failure should not abort startup"
    );
    assert!(ready.is_none(), "first-delivery signal should be consumed");
    assert_eq!(
        rx.await.expect("SMB ready signal should be published"),
        Ok(())
    );
    assert_eq!(
        handler
            .smb_keys
            .lock()
            .expect("smb key recorder lock")
            .len(),
        2,
        "expected renewal handler attempt"
    );
    server.abort();
}

#[tokio::test]
async fn rust_smb_stale_renewal_cert_is_ignored_until_fresh_csr_is_signed() {
    let handler = Arc::new(RecordingHandler::default());
    let (result, ready, rx, server) =
        run_smb_authority_fixture(AuthorityMode::ReorderedRenewal, handler.clone()).await;

    assert!(
        !result.expect("SMB reordered-renewal fixture should complete"),
        "SMB reordered renewal should not be fatal"
    );
    assert!(ready.is_none(), "first-delivery signal should be consumed");
    assert_eq!(
        rx.await.expect("SMB ready signal should be published"),
        Ok(())
    );
    let keys = handler
        .smb_keys
        .lock()
        .expect("smb key recorder lock")
        .clone();
    assert_eq!(
        keys.len(),
        2,
        "expected initial + valid renewal SMB deliveries"
    );
    assert_ne!(
        keys[0], keys[1],
        "fresh SMB renewal should install a new key"
    );
    server.abort();
}

#[tokio::test]
async fn rust_smb_startup_backoff_task_exits_when_aborted() {
    let (tx, mut rx) = tokio::sync::oneshot::channel();
    let handle = spawn_smb_loop(Some(tx));

    tokio::time::sleep(std::time::Duration::from_millis(25)).await;
    assert!(matches!(rx.try_recv(), Err(TryRecvError::Empty)));
    assert_task_aborts(handle, "SMB startup backoff").await;
}

#[tokio::test]
async fn rust_smb_renewal_backoff_task_exits_when_aborted() {
    let handle = spawn_smb_loop(None);

    tokio::time::sleep(std::time::Duration::from_millis(25)).await;
    assert_task_aborts(handle, "SMB renewal backoff").await;
}

#[test]
fn rust_public_key_validation_rejects_empty_cert_chain() {
    let chain = x509_pb::X509CertChain {
        id: "empty".to_string(),
        chain: Vec::new(),
        key: None,
    };

    let err = cert_leaf_matches_public_key(&chain, b"expected-key")
        .expect_err("empty cert chain must be rejected");

    assert!(err.to_string().contains("certificate chain is empty"));
}
