// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Package ghcr looks up an image tag's digest on a container registry that
// hands out pull tokens the way ghcr.io does (GET /token, then the
// distribution API's manifests endpoint with that bearer token).
package ghcr

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

var (
	// ErrNotPublished is returned when the registry has no such tag.
	ErrNotPublished = errors.New("not published")
	// ErrDenied is returned when the registry refuses the pull: a private
	// package asked for without a token that may read it.
	ErrDenied = errors.New("pull access denied")
)

// Digest returns the digest (sha256:...) of repo:tag on the registry at base
// (https://ghcr.io), repo being the path under the host
// (tacplus-dev/tacplus/build). With an empty token the pull token is asked for
// anonymously, as a public pull does; a token (a GitHub token with
// read:packages, for a private package) is sent as basic auth on that one
// request. A missing tag is ErrNotPublished; a refused pull, ErrDenied.
func Digest(client *http.Client, base, repo, tag, token string) (string, error) {
	q := url.Values{"scope": {"repository:" + repo + ":pull"}, "service": {"ghcr.io"}}
	req, err := http.NewRequest(http.MethodGet, base+"/token?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.SetBasicAuth("x-access-token", token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	var tok struct {
		Token string `json:"token"`
	}
	err = json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || err != nil || tok.Token == "" {
		return "", fmt.Errorf("no registry token (HTTP %d)", resp.StatusCode)
	}

	req, err = http.NewRequest(http.MethodHead, base+"/v2/"+repo+"/manifests/"+url.PathEscape(tag), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	resp, err = client.Do(req)
	if err != nil {
		return "", err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return "", ErrNotPublished
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", ErrDenied
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	if !strings.HasPrefix(digest, "sha256:") {
		return "", fmt.Errorf("the registry returned no digest")
	}
	return digest, nil
}
