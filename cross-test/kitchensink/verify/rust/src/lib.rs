// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

//! Kani harnesses over the generated Rust validate() of the kitchensink member
//! (tacplus#196). Each proves, for every value of the fields it makes symbolic,
//! that validate() accepts exactly the values the proto's annotations declare.
//! The generator is verified here once per release (`just verify`); services do
//! not re-prove generated code.
//!
//! Only messages whose validate() Kani can compile and run are proved here
//! (tacplus#211): not one that reaches `regex` (the string-format validators,
//! on which Kani 0.68 panics) or holds a map field (prost's HashMap default
//! makes a syscall Kani does not model), as `Sizes` does. Those are fuzzed
//! (cross-test/kitchensink/fuzz, testdata).

#[cfg(kani)]
mod proof {
    use kitchensink_model_types::services::kitchensink::{Bounds, Deep, Level1, Level2, Level3};

    // validate() formats the value it refuses into its error, and Kani cannot
    // finish core::fmt; is_ok() does not depend on the message's text.
    fn format_stub(_args: std::fmt::Arguments<'_>) -> String {
        String::new()
    }

    // Numeric bounds: assert_net_port, assert_gt/ge/lt/le on every integer
    // width, assert_numeric (range, ne, in, multiple_of), assert_enum_defined.
    #[kani::proof]
    #[kani::stub(std::fmt::format, format_stub)]
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

    // Deep nesting: validate() reaches the bound four messages down.
    #[kani::proof]
    #[kani::stub(std::fmt::format, format_stub)]
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
