// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"sort"
)

// ValidateMemberPort enforces the (spec.member_port) contract at generation
// time (tacplus#212). A service declares one black-box member transport: the
// member port (server TLS plus framed kernel tokens) or the deprecated
// mTLS-era (spec.smb), never both, since two transports for the same member
// traffic would leave which one guards it to the deployment.
func ValidateMemberPort(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	var both []string
	for _, fd := range dp.GetAllFiles() {
		for si := range fd.Services {
			svc := &fd.Services[si]
			if svc.IsMemberPort && svc.IsSMB {
				both = append(both, fd.Package+"."+svc.Name)
			}
		}
	}
	if len(both) == 0 {
		return nil
	}
	sort.Strings(both)
	return fmt.Errorf(
		"member port error: %s declares both (spec.member_port) and the deprecated (spec.smb); "+
			"member traffic goes through the member port only: remove (spec.smb)",
		both[0])
}

// RefuseMemberPort fails generation for a language that does not generate the
// member port yet when the service being generated (selfPackage) declares
// (spec.member_port): a member port that silently is not there would leave the
// member traffic it guards unguarded. issue names the follow-up that adds it.
func RefuseMemberPort(dp *DescriptorParser, selfPackage, language, issue string) error {
	if dp == nil || selfPackage == "" {
		return nil
	}
	for _, fd := range dp.GetAllFiles() {
		if fd.Package != selfPackage {
			continue
		}
		for si := range fd.Services {
			if fd.Services[si].IsMemberPort {
				return fmt.Errorf(
					"member port error: %s.%s declares (spec.member_port), which is generated for java only so far; "+
						"%s generates it with %s",
					fd.Package, fd.Services[si].Name, language, issue)
			}
		}
	}
	return nil
}
