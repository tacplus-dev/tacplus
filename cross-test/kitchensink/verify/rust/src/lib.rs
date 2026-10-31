// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

//! Kani harnesses over the generated Rust validate() of the kitchensink member
//! (tacplus#196). Each proves, for every value of the fields it makes symbolic,
//! that validate() accepts exactly the values the proto's annotations declare.
//! The generator is verified here once per release; services do not re-prove
//! generated code.

#[cfg(kani)]
mod proof {
    use kitchensink_model_types::services::kitchensink::{Bounds, Deep, Level1, Level2, Level3, Sizes};

    // Numeric bounds: assert_net_port, assert_gt/ge/lt/le on every integer
    // width, assert_numeric (range, ne, in, multiple_of), assert_enum_defined.
    #[kani::proof]
    #[kani::unwind(6)]
    fn bounds_validate_accepts_exactly_the_declared_ranges() {
        let mut m = Bounds::default();
        m.port = kani::any();
        m.gt_zero = kani::any();
        m.ge_minus_ten = kani::any();
        m.lt_hundred = kani::any();
        m.le_thousand = kani::any();
        m.window = kani::any();
        m.not_seven = kani::any();
        m.one_of = kani::any();
        m.even = kani::any();
        m.mode = kani::any();
        m.flag = kani::any();
        let declared = (0..=65535).contains(&m.port)
            && m.gt_zero > 0
            && m.ge_minus_ten >= -10
            && m.lt_hundred < 100
            && m.le_thousand <= 1000
            && (1..=64).contains(&m.window)
            && m.not_seven != 7
            && matches!(m.one_of, 1 | 2 | 4 | 8)
            && m.even % 2 == 0
            && (m.mode == 1 || m.mode == 2);
        assert_eq!(m.validate().is_ok(), declared);
    }

    fn string_of_len(n: usize) -> String {
        "x".repeat(n)
    }

    // Size bounds at their limits: a required string with a maximum, a
    // string with a minimum and a maximum, bytes and a repeated scalar.
    #[kani::proof]
    #[kani::unwind(20)]
    fn sizes_validate_accepts_exactly_the_declared_sizes() {
        let name_len: usize = kani::any();
        let code_len: usize = kani::any();
        let blob_len: usize = kani::any();
        let counts_len: usize = kani::any();
        kani::assume(name_len <= 10 && code_len <= 6 && blob_len <= 18 && counts_len <= 6);
        let mut m = Sizes::default();
        m.name = string_of_len(name_len);
        m.code = string_of_len(code_len);
        m.blob = vec![0u8; blob_len];
        m.counts = vec![0i32; counts_len];
        let declared = (1..=8).contains(&name_len)
            && (2..=4).contains(&code_len)
            && blob_len <= 16
            && counts_len <= 4;
        assert_eq!(m.validate().is_ok(), declared);
    }

    // Deep nesting: validate() reaches the bound four messages down.
    #[kani::proof]
    #[kani::unwind(4)]
    fn deep_validate_checks_the_innermost_bound() {
        let value: i32 = kani::any();
        let m = Deep {
            root: Some(Level1 {
                inner: Some(Level2 {
                    leaf: Some(Level3 { value, ..Default::default() }),
                    ..Default::default()
                }),
                ..Default::default()
            }),
            ..Default::default()
        };
        assert_eq!(m.validate().is_ok(), value >= 0);
    }
}
