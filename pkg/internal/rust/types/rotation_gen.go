// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// rustServesRotatingSibling reports whether a generated Rust server wrapper
// also serves the service's <Svc>Rotating sibling, and a generated S2S client
// of it rotates its streams (tacplus#322): every service-to-service service
// with a streaming rpc whose calls carry tokens. A c2d service has no tokens
// (its socket is the boundary), and an auth-disabled service has none to
// expire, so neither needs rotation. Parity with goServesRotatingSibling.
func rustServesRotatingSibling(service *parser.ServiceInfo) bool {
	if service == nil || !service.IsS2S || service.IsC2D || service.AuthDisabled {
		return false
	}
	for _, m := range service.Methods {
		if rustRotatingMethod(m) {
			return true
		}
	}
	return false
}

// rustRotatingMethod: a method of the sibling, a streaming rpc with a gRPC
// route of its own (an MCP tool has none).
func rustRotatingMethod(m *parser.MethodInfo) bool {
	return m != nil && !m.IsMcp && (m.ClientStreaming || m.ServerStreaming)
}

// rustDescriptorRotates is rustServesRotatingSibling over a descriptor
// parser's service, as the project-level scans see it.
func rustDescriptorRotates(svc *parser.ServiceDescriptor) bool {
	if svc == nil || !svc.IsS2S || svc.IsC2D || svc.AuthDisabled {
		return false
	}
	for i := range svc.Methods {
		if rustDescriptorRotatingMethod(&svc.Methods[i]) {
			return true
		}
	}
	return false
}

func rustDescriptorRotatingMethod(m *parser.MethodDescriptor) bool {
	return !m.IsMcp && (m.ClientStreaming || m.ServerStreaming)
}

// rustRotatingServices maps "pkg.Service" to the full gRPC paths of its
// sibling's methods ("/pkg.ServiceRotating/Method"), for every service that
// serves one. The ServiceManager ends each sibling leg at its token's exp
// (#260's layer) from this list.
func rustRotatingServices(flatProtoDir string) (map[string][]string, error) {
	dp, err := parser.NewDescriptorParser(flatProtoDir)
	if err != nil {
		return nil, fmt.Errorf("rotating service scan: parse %q: %w", flatProtoDir, err)
	}
	out := make(map[string][]string)
	for _, fd := range dp.GetAllFiles() {
		if fd == nil {
			continue
		}
		for si := range fd.Services {
			svc := &fd.Services[si]
			if !rustDescriptorRotates(svc) {
				continue
			}
			key := fd.Package + "." + svc.Name
			for mi := range svc.Methods {
				if m := &svc.Methods[mi]; rustDescriptorRotatingMethod(m) {
					out[key] = append(out[key], fmt.Sprintf("/%s.%sRotating/%s", fd.Package, svc.Name, m.Name))
				}
			}
		}
	}
	return out, nil
}

// detectRotation reports whether some generated wrapper or S2S client can
// rotate: only then is the rotation chassis crate emitted, so no other build
// compiles it. It answers a superset of what is generated, never a subset: any
// rotating service of any proto in the build, authored or sys. (Narrowing it to
// the services a build serves or calls by name missed vservice clients, such
// as the record-egress query clients, whose generated client rotates.)
func detectRotation(config *tactype.Config) (bool, error) {
	if config.FlatProtoDir == "" {
		return false, nil
	}
	dp, err := parser.NewDescriptorParser(config.FlatProtoDir)
	if err != nil {
		return false, fmt.Errorf("rotation scan: parse proto descriptors in %q: %w", config.FlatProtoDir, err)
	}
	for _, fd := range dp.GetAllFiles() {
		if fd == nil {
			continue
		}
		for si := range fd.Services {
			if rustDescriptorRotates(&fd.Services[si]) {
				return true, nil
			}
		}
	}
	return false, nil
}

// rustRotationSibling renders the wrapper's half of the <Svc>Rotating sibling:
// its name, the ValidationWrapper's rotating_sibling (which the
// ServiceManager serves beside the plain service), and rotation_admit, the
// rotation chassis' Admit for this service. rotation_admit applies, per
// streaming method, exactly the check the method's own wrapper applies at
// open (rustAuthLine), so a rotation passes the same (spec.rpc) policy the
// opening leg's call did:
//
//   - an opening leg only authenticates (signature, audience, exp, the tenant
//     door); the wrapper then runs the method's full check on the handler's
//     call, as it does on the plain method;
//   - a rotation runs the full check: authorize, the auth_any_user check, and
//     a server stream's target_namespace_field check against the stream's
//     request (UNAVAILABLE, retryable, before the request has arrived);
//   - an auth-disabled method carries no token, so it has nothing to rotate
//     onto: a rotation is refused.
//
// Parity with goRotationAdmit (Go's rotation_gen.go).
func rustRotationSibling(service *parser.ServiceInfo, servicePackage, parentDir string, crossPkgTypes map[string]string) string {
	if !rustServesRotatingSibling(service) {
		return ""
	}
	tonicName := tonicServiceTypeName(service.Name)
	plainService := servicePackage + "." + service.Name
	var methods []string
	var b strings.Builder
	fmt.Fprintf(&b, `
/// %[1]sRotating, the rotating sibling of %[1]s (tacplus#322): the same
/// streaming methods, every one bidi of rotation::Envelope, whose streams the
/// generated clients carry across token lifetimes. The ServiceManager serves
/// it beside the plain service.
pub struct %[2]sRotating;

impl rotation::SiblingName for %[2]sRotating {
    const NAME: &'static str = "%[3]sRotating";
}

impl ValidationWrapper {
    /// The rotating sibling of this service, running `+"`plain`"+` (the
    /// generated server of this wrapper, as the ServiceManager serves it) on
    /// one logical stream per session, so a handler sees one stream for its
    /// whole life.
    pub fn rotating_sibling<S>(&self, plain: S) -> rotation::Sibling<S, %[2]sRotating>
    where
        S: rotation::PlainService,
        <S as tonic::codegen::Service<tonic::codegen::http::Request<tonic::body::Body>>>::Future: Send + 'static,
    {
        let wrapper = self.clone();
        let admit: rotation::Admit = Arc::new(move |method: String, authorization: Option<String>, first: Option<tonic::codegen::Bytes>, rotating: bool| -> tonic::codegen::BoxFuture<(), tonic::Status> {
            let wrapper = wrapper.clone();
            Box::pin(async move { wrapper.rotation_admit(&method, authorization.as_deref(), first, rotating).await })
        });
        rotation::Sibling::new("%[3]s", ROTATING_METHODS, plain, admit)
    }

    /// Admits a leg of %[1]sRotating: an opening leg is authenticated, and a
    /// rotation must pass the method's own open-time policy.
    #[allow(unused_variables)]
    async fn rotation_admit(
        &self,
        method: &str,
        authorization: Option<&str>,
        first: Option<tonic::codegen::Bytes>,
        rotating: bool,
    ) -> Result<(), tonic::Status> {
        let guard = self.authorizer.read().await;
        let auth = guard.as_ref();
        match method {`, service.Name, tonicName, plainService)
	for _, m := range service.Methods {
		if !rustRotatingMethod(m) {
			continue
		}
		methods = append(methods, fmt.Sprintf("%q", m.Name))
		fullMethod := fmt.Sprintf("/%s/%s.%s/%s", parentDir, servicePackage, service.Name, m.Name)
		fmt.Fprintf(&b, "\n            %q => {", m.Name)
		if m.AuthDisabled {
			b.WriteString(`
                // Auth is disabled: no token, no exp, nothing to rotate onto.
                if rotating {
                    return Err(tonic::Status::permission_denied(""));
                }
                Ok(())
            }`)
			continue
		}
		b.WriteString(`
                // Fail closed: a mandatory-auth wrapper with no authorizer admits nothing.
                let Some(auth) = auth else { return Err(tonic::Status::unauthenticated("")) };`)
		fmt.Fprintf(&b, `
                if !rotating {
                    return auth.authenticate(%q, authorization).await.map(|_| ());
                }`, fullMethod)
		switch {
		case m.AuthAnyUser:
			if target := strings.TrimSpace(m.TargetNamespaceField); target != "" {
				fmt.Fprintf(&b, "\n                auth.check_at_or_below(%q, authorization, %q).await.map(|_| ())", fullMethod, target)
			} else {
				fmt.Fprintf(&b, "\n                auth.authenticate(%q, authorization).await.map(|_| ())", fullMethod)
			}
		case m.TargetNamespaceField != "" && !m.ClientStreaming:
			inputType, _ := resolveRustTypeQualified(m, servicePackage, crossPkgTypes)
			fmt.Fprintf(&b, `
                auth.authorize(%[1]q, authorization).await?;
                // The stream's request has not reached the handler yet: retryable.
                let Some(first) = first else { return Err(tonic::Status::unavailable("")) };
                let request = <%[2]s as prost::Message>::decode(first)
                    .map_err(|_| tonic::Status::permission_denied(""))?;
                auth.check_at_or_below(%[1]q, authorization, &request.%[3]s).await.map(|_| ())`,
				fullMethod, inputType, strutil.ToSnakeCase(m.TargetNamespaceField))
		default:
			fmt.Fprintf(&b, "\n                auth.authorize(%q, authorization).await.map(|_| ())", fullMethod)
		}
		b.WriteString("\n            }")
	}
	fmt.Fprintf(&b, `
            _ => Err(tonic::Status::unimplemented("")),
        }
    }
}

/// The methods of %sRotating: the service's streaming rpcs.
const ROTATING_METHODS: &[&str] = &[%s];
`, service.Name, strings.Join(methods, ", "))
	return b.String()
}
