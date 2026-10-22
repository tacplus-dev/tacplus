// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateSmbAuthority enforces the SMB authority contract (SPIFFE plan D7) at
// generation time: if any (spec.smb) service is declared, exactly one consumer
// service must declare itself the SMB CA authority via (spec.smb_authority).
//
// SMB services obtain their isolated per-SMB CA + member cert from that
// authority; without a declared authority there is nothing to issue them, so
// generation FAILS rather than emitting an SMB service that can never establish
// its mTLS. More than one authority is also rejected — the authority is a single
// designated role (like token_minter), not a set. tacplus does not implement the
// authority; it only requires that one be designated.
func ValidateSmbAuthority(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}

	var smbServices, authorities []string
	for _, fd := range dp.GetAllFiles() {
		for si := range fd.Services {
			svc := &fd.Services[si]
			fq := fd.Package + "." + svc.Name
			if svc.IsSMB {
				smbServices = append(smbServices, fq)
			}
			if svc.IsSMBAuthority {
				authorities = append(authorities, fq)
			}
		}
	}

	if len(smbServices) == 0 {
		return nil
	}

	sort.Strings(smbServices)
	sort.Strings(authorities)

	switch {
	case len(authorities) == 0:
		return fmt.Errorf(
			"smb authority error: %d (spec.smb) service(s) declared [%s] but no "+
				"(spec.smb_authority) service — declare exactly one consumer service as "+
				"the SMB CA authority so SMB members can obtain their per-SMB CA + cert",
			len(smbServices), strings.Join(smbServices, ", "))
	case len(authorities) > 1:
		return fmt.Errorf(
			"smb authority error: %d (spec.smb_authority) services declared [%s] — "+
				"exactly one SMB CA authority is allowed",
			len(authorities), strings.Join(authorities, ", "))
	}

	return nil
}

// SmbAuthorityServiceID returns the service identity used by generated SMB
// members to reach the service annotated with (spec.smb_authority). Today the
// service identity aligns with the declaring proto package, matching the
// generated S2S target-service convention.
func SmbAuthorityServiceID(dp *DescriptorParser) (string, error) {
	if dp == nil {
		return "", nil
	}

	var smbServices, authorities []string
	authorityPackages := make(map[string]struct{})
	for _, fd := range dp.GetAllFiles() {
		for si := range fd.Services {
			svc := &fd.Services[si]
			fq := fd.Package + "." + svc.Name
			if svc.IsSMB {
				smbServices = append(smbServices, fq)
			}
			if svc.IsSMBAuthority {
				authorities = append(authorities, fq)
				authorityPackages[fd.Package] = struct{}{}
			}
		}
	}
	if len(smbServices) == 0 {
		return "", nil
	}
	if len(authorities) != 1 {
		if err := ValidateSmbAuthority(dp); err != nil {
			return "", err
		}
		return "", fmt.Errorf("smb authority error: expected exactly one (spec.smb_authority), got %d", len(authorities))
	}
	for pkg := range authorityPackages {
		return pkg, nil
	}
	return "", fmt.Errorf("smb authority error: internal missing authority package")
}
