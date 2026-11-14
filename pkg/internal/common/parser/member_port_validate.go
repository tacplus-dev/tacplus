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
