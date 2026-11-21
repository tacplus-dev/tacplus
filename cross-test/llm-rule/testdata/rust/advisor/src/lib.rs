// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

//! A green advisor (tacplus#909): it reaches its model only through the
//! generated model port, the one crate of the service its manifest names.

use std::sync::Arc;

use zerorpc_model_advisor::{wire, ModelPort, ModelPortError};

/// Asks the model through the port, on the trace of the request it answers.
pub async fn ask(model: Arc<dyn ModelPort>, trace_id: &str, prompt: &str) -> Result<String, ModelPortError> {
    let response = model
        .generate(trace_id, wire::CompleteRequest { prompt: prompt.to_string(), ..Default::default() })
        .await?;
    Ok(response.text)
}

#[cfg(test)]
mod tests {
    use super::*;
    use zerorpc_model_advisor::async_trait;

    /// The core's stand-in: what the advisor asked, on which trace.
    struct Echo;

    #[async_trait]
    impl ModelPort for Echo {
        async fn generate(
            &self,
            trace_id: &str,
            request: wire::CompleteRequest,
        ) -> Result<wire::CompleteResponse, ModelPortError> {
            Ok(wire::CompleteResponse { text: format!("{trace_id}:{}", request.prompt), ..Default::default() })
        }
    }

    #[tokio::test]
    async fn the_advisor_asks_through_the_port_on_its_trace() {
        assert_eq!(ask(Arc::new(Echo), "trace-1", "ping").await.unwrap(), "trace-1:ping");
    }
}
