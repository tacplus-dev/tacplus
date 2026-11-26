#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# The RUSTC_WRAPPER of every cross-test leg when sccache is on PATH
# (cross-test/lib/cargo-target.sh, tacplus#979): sccache, without the leg's
# CARGO_TARGET_DIR. sccache keys a compile on the CARGO_ variables of its
# environment, and each job slot of a gate run builds in a target directory
# of its own (cargo locks one for a whole build), so with it every slot
# compiled the same dependencies again. rustc never reads it.
exec env -u CARGO_TARGET_DIR sccache "$@"
