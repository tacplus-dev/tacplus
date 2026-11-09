// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// A Sizes that validate() accepts holds every declared size bound.
#![no_main]
use kitchensink_model_types::services::kitchensink::Sizes;
use libfuzzer_sys::fuzz_target;
use prost::Message;

fuzz_target!(|data: &[u8]| {
    let Ok(m) = Sizes::decode(data) else { return };
    if m.validate().is_err() {
        return;
    }
    assert!((1..=8).contains(&m.name.len()), "{m:?}");
    assert!(m.blob.len() <= 16 && m.counts.len() <= 4 && m.tags.len() <= 3, "{m:?}");
    assert!(m.tags.iter().all(|t| t.len() <= 5), "{m:?}");
    assert!(m.labels.len() <= 2 && (2..=4).contains(&m.code.len()), "{m:?}");
    assert!(m.nickname.as_ref().map_or(true, |n| n.len() <= 6), "{m:?}");
    let back = Sizes::decode(m.encode_to_vec().as_slice()).expect("re-decode");
    assert_eq!(m, back);
});
