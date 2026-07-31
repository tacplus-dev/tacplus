// Copyright (c) 2026 Second Star Cavalier LLC

package packages

// PackageDefinition defines a package that can be managed across languages
type PackageDefinition struct {
	Name        string              // Common name (e.g., "protobuf", "jwt")
	Description string              // What this package is for
	Languages   []string            // Which languages support this package
	Mappings    map[string][]string // language -> package names
}

// KnownPackages is the canonical list of packages that tacplus manages
// This list should be updated when adding support for new dependencies
var KnownPackages = []PackageDefinition{
	{
		Name:        "protobuf",
		Description: "Protocol Buffers - serialization format",
		Languages:   []string{"go", "java", "python", "rust", "typescript"},
		Mappings: map[string][]string{
			"go":         {"google.golang.org/protobuf"},
			"java":       {"com.google.protobuf:protobuf-java", "com.google.protobuf:protobuf-java-util"},
			"python":     {"protobuf"},
			"rust":       {"prost", "prost-types", "prost-wkt-types"},
			"typescript": {"@bufbuild/protobuf", "@bufbuild/protoc-gen-es"},
		},
	},
	{
		Name:        "grpc",
		Description: "gRPC - RPC framework",
		Languages:   []string{"go", "java"},
		Mappings: map[string][]string{
			"go":   {"google.golang.org/grpc", "google.golang.org/genproto/googleapis/rpc"},
			"java": {"io.grpc:grpc-netty-shaded", "io.grpc:grpc-protobuf", "io.grpc:grpc-stub"},
		},
	},
	{
		Name:        "jwt",
		Description: "JSON Web Tokens - authentication",
		Languages:   []string{"go", "java"},
		Mappings: map[string][]string{
			"go":   {"github.com/golang-jwt/jwt/v5"},
			"java": {"io.jsonwebtoken:jjwt-api", "io.jsonwebtoken:jjwt-impl", "io.jsonwebtoken:jjwt-gson"},
		},
	},
	{
		Name:        "yaml",
		Description: "YAML - serialization format",
		Languages:   []string{"go", "python"},
		Mappings: map[string][]string{
			"go":     {"gopkg.in/yaml.v3"},
			"python": {"PyYAML"},
		},
	},
	{
		Name:        "toml",
		Description: "TOML - configuration format",
		Languages:   []string{"go", "python", "java", "rust"},
		Mappings: map[string][]string{
			"go":     {"github.com/pelletier/go-toml/v2"},
			"python": {"tomli", "tomli-w"},
			"java":   {"com.moandjiezana.toml:toml4j"},
			"rust":   {"toml"},
		},
	},
	{
		Name:        "json",
		Description: "JSON - serialization utilities",
		Languages:   []string{"java", "rust"},
		Mappings: map[string][]string{
			"java": {"com.google.code.gson:gson"},
			"rust": {"serde_json"},
		},
	},
	{
		Name:        "serde",
		Description: "Serde - Rust serialization framework",
		Languages:   []string{"rust"},
		Mappings: map[string][]string{
			"rust": {"serde", "serde_json", "serde_yaml"},
		},
	},
	{
		Name:        "gin",
		Description: "Gin - Go web framework",
		Languages:   []string{"go"},
		Mappings: map[string][]string{
			"go": {"github.com/gin-gonic/gin"},
		},
	},
	{
		Name:        "fastapi",
		Description: "FastAPI - Python web framework",
		Languages:   []string{"python"},
		Mappings: map[string][]string{
			"python": {"fastapi", "uvicorn"},
		},
	},
	{
		Name:        "axum",
		Description: "Axum - Rust web framework",
		Languages:   []string{"rust"},
		Mappings: map[string][]string{
			"rust": {"axum", "tokio", "tower"},
		},
	},
	{
		Name:        "express",
		Description: "Express - TypeScript/Node web framework",
		Languages:   []string{"typescript"},
		Mappings: map[string][]string{
			"typescript": {"express", "@types/express"},
		},
	},
	{
		Name:        "logging",
		Description: "Logging frameworks",
		Languages:   []string{"go", "java"},
		Mappings: map[string][]string{
			"go":   {"go.uber.org/zap"},
			"java": {"org.slf4j:slf4j-api", "org.slf4j:slf4j-simple", "ch.qos.logback:logback-classic", "ch.qos.logback:logback-core"},
		},
	},
	{
		Name:        "validation",
		Description: "Data validation",
		Languages:   []string{"python", "typescript"},
		Mappings: map[string][]string{
			"python":     {"pydantic"},
			"typescript": {"zod"},
		},
	},
	{
		Name:        "cryptography",
		Description: "Cryptographic primitives and X.509 support",
		Languages:   []string{"python"},
		Mappings: map[string][]string{
			"python": {"cryptography"},
		},
	},
	{
		Name:        "http-client",
		Description: "HTTP client libraries",
		Languages:   []string{"typescript", "python"},
		Mappings: map[string][]string{
			"typescript": {"axios"},
			"python":     {"httpx", "requests", "urllib3"},
		},
	},
	{
		Name:        "websocket",
		Description: "WebSocket client libraries",
		Languages:   []string{"go", "python"},
		Mappings: map[string][]string{
			"go":     {"golang.org/x/net"},
			"python": {"websockets"},
		},
	},
	{
		Name:        "async",
		Description: "Async utilities",
		Languages:   []string{"go", "rust"},
		Mappings: map[string][]string{
			"go":   {"golang.org/x/sync"},
			"rust": {"bytes", "regex"},
		},
	},
	{
		Name:        "mux",
		Description: "Connection multiplexing",
		Languages:   []string{"go"},
		Mappings: map[string][]string{
			"go": {"github.com/soheilhy/cmux"},
		},
	},
	{
		Name:        "testing",
		Description: "Testing frameworks",
		Languages:   []string{"java"},
		Mappings: map[string][]string{
			"java": {"org.junit.jupiter:junit-jupiter", "org.mockito:mockito-core", "org.mockito:mockito-junit-jupiter", "org.assertj:assertj-core"},
		},
	},
	{
		Name:        "javalin",
		Description: "Javalin - Java web framework",
		Languages:   []string{"java"},
		Mappings: map[string][]string{
			"java": {"io.javalin:javalin"},
		},
	},
	{
		Name:        "yaml-java",
		Description: "YAML for Java",
		Languages:   []string{"java"},
		Mappings: map[string][]string{
			"java": {"org.yaml:snakeyaml"},
		},
	},
	{
		Name:        "annotations",
		Description: "Java annotations",
		Languages:   []string{"java"},
		Mappings: map[string][]string{
			"java": {"org.apache.tomcat:annotations-api"},
		},
	},
	{
		Name:        "maven-plugins",
		Description: "Maven build plugins",
		Languages:   []string{"java"},
		Mappings: map[string][]string{
			"java": {"org.apache.maven.plugins:maven-shade-plugin", "org.apache.maven.plugins:maven-compiler-plugin", "org.apache.maven.plugins:maven-surefire-plugin"},
		},
	},
}
