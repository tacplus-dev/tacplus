// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The parsing and recursive validators (formats, conditionals, the deep tree,
// every scalar shape and map) refuse or accept anything that decodes; they
// never panic.
#![no_main]
use kitchensink_model_types::services::kitchensink::{
    Conditional, Formats, GetBoundsRequest, Maps, OptionalScalars, RepeatedScalars,
};
use libfuzzer_sys::fuzz_target;
use prost::Message;

fuzz_target!(|data: &[u8]| {
    if let Ok(m) = GetBoundsRequest::decode(data) {
        let _ = m.validate();
        let back = GetBoundsRequest::decode(m.encode_to_vec().as_slice()).expect("re-decode");
        assert_eq!(m, back);
    }
    if let Ok(m) = Formats::decode(data) {
        let _ = m.validate();
    }
    if let Ok(m) = Conditional::decode(data) {
        let _ = m.validate();
    }
    if let Ok(m) = RepeatedScalars::decode(data) {
        let _ = m.validate();
    }
    if let Ok(m) = OptionalScalars::decode(data) {
        let _ = m.validate();
    }
    if let Ok(m) = Maps::decode(data) {
        let _ = m.validate();
    }
});
