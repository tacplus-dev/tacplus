// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// A Bounds that decodes is accepted by validate() exactly when its declared
// bounds hold, and survives a round trip.
#![no_main]
use kitchensink_model_types::services::kitchensink::Bounds;
use libfuzzer_sys::fuzz_target;
use prost::Message;

fuzz_target!(|data: &[u8]| {
    let Ok(m) = Bounds::decode(data) else { return };
    let declared = (0..=65535).contains(&m.port)
        && m.gt_zero > 0
        && m.ge_minus_ten >= -10
        && m.lt_hundred < 100
        && m.le_thousand <= 1000
        && (1..=64).contains(&m.window)
        && m.not_seven != 7
        && matches!(m.one_of, 1 | 2 | 4 | 8)
        && m.even % 2 == 0
        && (m.mode == 1 || m.mode == 2)
        && m.trace_id.len() <= 32768;
    assert_eq!(m.validate().is_ok(), declared, "{m:?}");
    let back = Bounds::decode(m.encode_to_vec().as_slice()).expect("re-decode");
    assert_eq!(m, back);
});
