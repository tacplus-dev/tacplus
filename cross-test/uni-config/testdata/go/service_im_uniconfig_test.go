// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package service_im

import (
	"testing"

	grpcconfig_pb "example.com/uniconfig/tacplus/model/types/sys/grpcconfig"
)

// The projected Kubernetes token file is config (k8s_token_file), so the
// uni-config shows it; a KERNEL_K8S_TOKEN_FILE environment variable moves
// nothing.
func TestGoK8sTokenFileComesFromConfig(t *testing.T) {
	t.Setenv("KERNEL_K8S_TOKEN_FILE", "/from/the/environment")

	_, k8sTokenFile := bootstrapSources(&grpcconfig_pb.Config{K8STokenFile: "/from/the/config"})
	if k8sTokenFile != "/from/the/config" {
		t.Fatalf("k8s token file = %q, want the config's", k8sTokenFile)
	}

	_, k8sTokenFile = bootstrapSources(&grpcconfig_pb.Config{})
	if k8sTokenFile != "" {
		t.Fatalf("with no k8s_token_file configured the environment was read: %q", k8sTokenFile)
	}
}

// KERNEL_BOOTSTRAP_TOKEN_FILE is not deployment config: a parent workload
// writes it for the child process it starts, so it stays an env var.
func TestGoBootstrapTokenFileStaysAParentHandoff(t *testing.T) {
	t.Setenv("KERNEL_BOOTSTRAP_TOKEN_FILE", "/from/the/parent")
	bootstrapTokenFile, _ := bootstrapSources(&grpcconfig_pb.Config{})
	if bootstrapTokenFile != "/from/the/parent" {
		t.Fatalf("bootstrap token file = %q", bootstrapTokenFile)
	}
}
