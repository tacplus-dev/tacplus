// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

//! The core of a service with an advisor (tacplus#909): it builds the
//! generated adapter over its ServiceManager and passes the advisor only the
//! port. Built, not run: building it builds the model crate, whose model port
//! checks the advisor's manifest.

use std::sync::Arc;

use zerorpc_model::advisor_port::{ModelPort, ModelPortAdapter};
use zerorpc_model::s2sm::ServiceManager;

fn model_for_the_advisor(sm: Arc<ServiceManager>) -> Arc<dyn ModelPort> {
    Arc::new(ModelPortAdapter::new(sm))
}

#[tokio::main]
async fn main() {
    if std::env::args().nth(1).as_deref() != Some("run") {
        return;
    }
    let sm = Arc::new(ServiceManager::new().await.expect("ServiceManager"));
    let answer = advisor::ask(model_for_the_advisor(sm), "trace", "ping").await;
    println!("{answer:?}");
}
