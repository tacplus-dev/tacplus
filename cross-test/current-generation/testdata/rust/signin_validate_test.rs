// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the generated sign-in config (tacplus#224). NOT generated:
// run.sh copies this file into the generated crate's tests/ directory and runs
// it with `cargo test --test signin_validate_test`.
//
// What it pins: the generated validation of sys/signin.proto refuses a public
// suffix registered as a domain, a SAML IdP and an address-shaped subject, and
// accepts a whole valid configuration, as the Go self-test does.

use proto_model::types::sys::signin::*;

fn valid_tenant() -> Tenant {
    Tenant {
        providers: vec![Provider::Google as i32, Provider::Custom as i32],
        idp: Some(OidcIdp {
            protocol: IdpProtocol::Oidc as i32,
            issuer: "https://login.microsoftonline.com/9188040d-6c67-4c5b-b112-36a304b66dad/v2.0".into(),
            client_id: "a1b2c3".into(),
            multi_tenant: true,
            idp_tenant: "9188040d-6c67-4c5b-b112-36a304b66dad".into(),
            subject_claim: "oid".into(),
            subject_non_reassignable: true,
        }),
        domains: vec![Domain {
            name: "acme.co.uk".into(),
            include_subdomains: true,
            state: DomainState::Verified as i32,
            verified_at: 1,
            lapsed_at: 0,
        }],
        join_mode: JoinMode::Automatic as i32,
        merge_redirect_seconds: DEFAULT_MERGE_REDIRECT_SECONDS,
        registration_grace_seconds: DEFAULT_REGISTRATION_GRACE_SECONDS,
        max_proof_age_seconds: 43200,
        dns_lapse_grace_seconds: 604800,
    }
}

#[test]
fn signin_defaults_are_thirty_days() {
    assert_eq!(DEFAULT_MERGE_REDIRECT_SECONDS, 30 * 86400);
    assert_eq!(DEFAULT_REGISTRATION_GRACE_SECONDS, 30 * 86400);
}

#[test]
fn signin_valid_config_accepted() {
    valid_tenant().validate().expect("a valid tenant was refused");
    Deployment { account_model: AccountModel::Single as i32 }.validate().expect("a valid deployment was refused");
    assert!(Deployment::default().validate().is_err(), "a deployment without an account model was accepted");
}

#[test]
fn signin_refusals() {
    let cases: Vec<(&str, Box<dyn Fn(&mut Tenant)>, &str)> = vec![
        ("public suffix", Box::new(|t| t.domains[0].name = "co.uk".into()), "registrable"),
        ("private-section public suffix", Box::new(|t| t.domains[0].name = "github.io".into()), "registrable"),
        ("SAML IdP", Box::new(|t| t.idp.as_mut().unwrap().protocol = IdpProtocol::Saml as i32), "protocol"),
        ("reassignable subject", Box::new(|t| t.idp.as_mut().unwrap().subject_non_reassignable = false), "subject_non_reassignable"),
        ("multi-tenant IdP without its tenant", Box::new(|t| t.idp.as_mut().unwrap().idp_tenant.clear()), "idp_tenant"),
        ("no provider", Box::new(|t| t.providers.clear()), "providers"),
    ];
    for (name, mutate, want) in cases {
        let mut t = valid_tenant();
        mutate(&mut t);
        match t.validate() {
            Ok(()) => panic!("{name}: accepted"),
            Err(e) => assert!(e.contains(want), "{name}: refused for another reason: {e}"),
        }
    }
}

#[test]
fn signin_address_shaped_subject_refused() {
    let ok = Subject { issuer: "https://accounts.google.com".into(), idp_tenant: String::new(), subject: "110169484474386276334".into() };
    ok.validate().expect("an opaque subject was refused");
    let bad = Subject { subject: "jody@acme.com".into(), ..ok };
    let err = bad.validate().expect_err("an address-shaped subject was accepted");
    assert!(err.contains("address-shaped"), "{err}");
}
