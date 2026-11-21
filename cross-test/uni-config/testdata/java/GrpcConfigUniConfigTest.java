// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package com.example.uniconfig.model.chassis.grpcconfig;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.example.uniconfig.model.types.svc.uniconfig.Helpers;
import com.example.uniconfig.model.types.svc.uniconfig.Uniconfig.Secrets;
import com.example.uniconfig.model.types.sys.errors.ProtoException;
import com.example.uniconfig.model.types.sys.grpcconfig.Grpcconfig.Config;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

class GrpcConfigUniConfigTest {
    private static final String BOOTSTRAP_YAML =
            "instance_id: from-file\nk8s_token_file: /var/run/secrets/tacplus/kernel/token\n";

    @TempDir
    Path dir;

    private Path write(String name, String body) throws Exception {
        Path path = dir.resolve(name);
        Files.writeString(path, body, StandardCharsets.UTF_8);
        return path;
    }

    @Test
    void javaConfigPathsArePlatformConstants() {
        assertEquals("/etc/tacplus/bootstrap.yaml",
                com.example.uniconfig.model.types.sys.grpcconfig.Helpers.BOOTSTRAP_CONFIG_PATH);
        assertEquals("/etc/tacplus/secrets.yaml",
                com.example.uniconfig.model.types.sys.grpcconfig.Helpers.SECRETS_PATH);
    }

    @Test
    void javaLoaderReadsTheFixedPath() throws Exception {
        Path path = write("bootstrap.yaml", BOOTSTRAP_YAML);
        Config cfg = GrpcConfigLoader.loadConfig(new String[] {"svc"}, path.toString());
        assertEquals("from-file", cfg.getInstanceId(), "instance_id must come from the fixed path");
        assertEquals("/var/run/secrets/tacplus/kernel/token", cfg.getK8STokenFile(),
                "k8s_token_file must come from config");
    }

    @Test
    void javaLoaderWithoutAnyConfigNamesTheFixedPath() {
        String missing = dir.resolve("bootstrap.yaml").toString();
        IllegalArgumentException err = assertThrows(IllegalArgumentException.class,
                () -> GrpcConfigLoader.loadConfig(new String[] {"svc"}, missing));
        assertTrue(err.getMessage().contains(missing),
                "the error must name the fixed path " + missing + ": " + err.getMessage());
        assertTrue(err.getMessage().contains("-c/--config"),
                "the error must name the -c/--config flag: " + err.getMessage());
    }

    @Test
    void javaLoaderConfigFlagWinsOverTheFixedPath() throws Exception {
        Path fixed = write("bootstrap.yaml", "instance_id: fixed\n");
        Path flag = write("flag.yaml", "instance_id: flag\n");
        for (String opt : new String[] {"-c", "--config"}) {
            Config cfg = GrpcConfigLoader.loadConfig(new String[] {"svc", opt, flag.toString()}, fixed.toString());
            assertEquals("flag", cfg.getInstanceId(), opt + " must win over the fixed path");
        }
    }

    @Test
    void javaLoaderUnreadableConfigFlagIsAnError() throws Exception {
        Path fixed = write("bootstrap.yaml", "instance_id: fixed\n");
        String missing = dir.resolve("absent.yaml").toString();
        assertThrows(java.io.IOException.class,
                () -> GrpcConfigLoader.loadConfig(new String[] {"svc", "-c", missing}, fixed.toString()));
    }

    // A service reads its Secrets from the platform's secrets file with the
    // generated reader, not from environment variables.
    @Test
    void javaSecretsFromFile() throws Exception {
        Path path = write("secrets.yaml", "api_key: s3cr3t-from-the-uni-config\n");
        Secrets secrets = Helpers.secretsFromFile(path.toString());
        assertEquals("s3cr3t-from-the-uni-config", secrets.getApiKey());

        String missing = dir.resolve("secrets-absent.yaml").toString();
        ProtoException err = assertThrows(ProtoException.class, () -> Helpers.secretsFromFile(missing));
        assertTrue(err.getMessage().contains(missing),
                "a missing secrets file must fail naming " + missing + ": " + err.getMessage());
    }
}
