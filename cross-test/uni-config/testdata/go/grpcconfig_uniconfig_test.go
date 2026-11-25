// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package grpcconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	uniconfig_pb "example.com/uniconfig/tacplus/model/types/svc/uniconfig"
	grpcconfig_pb "example.com/uniconfig/tacplus/model/types/sys/grpcconfig"
)

const bootstrapYAML = "instance_id: from-file\nk8s_token_file: /var/run/secrets/tacplus/kernel/token\n"

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGoConfigPathsArePlatformConstants(t *testing.T) {
	if grpcconfig_pb.BootstrapConfigPath != "/etc/tacplus/bootstrap.yaml" {
		t.Fatalf("BootstrapConfigPath = %q", grpcconfig_pb.BootstrapConfigPath)
	}
	if grpcconfig_pb.SecretsPath != "/etc/tacplus/secrets.yaml" {
		t.Fatalf("SecretsPath = %q", grpcconfig_pb.SecretsPath)
	}
}

func TestGoLoaderReadsTheFixedPath(t *testing.T) {
	path := writeFile(t, "bootstrap.yaml", bootstrapYAML)
	cfg, err := loadConfig([]string{"svc"}, path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.GetInstanceId() != "from-file" {
		t.Fatalf("instance_id = %q, want the fixed path's", cfg.GetInstanceId())
	}
	if cfg.GetK8STokenFile() != "/var/run/secrets/tacplus/kernel/token" {
		t.Fatalf("k8s_token_file = %q, want it from config", cfg.GetK8STokenFile())
	}
}

func TestGoLoaderIgnoresTheBootstrapConfigEnvVar(t *testing.T) {
	t.Setenv("BOOTSTRAP_CONFIG", writeFile(t, "elsewhere.yaml", bootstrapYAML))
	missing := filepath.Join(t.TempDir(), "bootstrap.yaml")
	cfg, err := loadConfig([]string{"svc"}, missing)
	if err == nil {
		t.Fatalf("BOOTSTRAP_CONFIG moved the config: loaded %v", cfg)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("the error must name the fixed path %s: %v", missing, err)
	}
}

func TestGoLoaderConfigFlagWinsOverTheFixedPath(t *testing.T) {
	fixed := writeFile(t, "bootstrap.yaml", "instance_id: fixed\n")
	flag := writeFile(t, "flag.yaml", "instance_id: flag\n")
	for _, args := range [][]string{{"svc", "-c", flag}, {"svc", "--config", flag}} {
		cfg, err := loadConfig(args, fixed)
		if err != nil {
			t.Fatalf("loadConfig(%v): %v", args, err)
		}
		if cfg.GetInstanceId() != "flag" {
			t.Fatalf("loadConfig(%v): instance_id = %q, want the flag's", args, cfg.GetInstanceId())
		}
	}
}

// A service reads its Secrets from the platform's secrets file with the
// generated reader, not from environment variables.
func TestGoSecretsFromFile(t *testing.T) {
	path := writeFile(t, "secrets.yaml", "api_key: s3cr3t-from-the-uni-config\n")
	secrets, err := uniconfig_pb.SecretsFromFile(path)
	if err != nil {
		t.Fatalf("SecretsFromFile: %v", err)
	}
	if secrets.GetApiKey() != "s3cr3t-from-the-uni-config" {
		t.Fatalf("api_key = %q", secrets.GetApiKey())
	}

	missing := filepath.Join(t.TempDir(), "secrets.yaml")
	if _, err := uniconfig_pb.SecretsFromFile(missing); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("a missing secrets file must fail naming %s, got %v", missing, err)
	}
}
