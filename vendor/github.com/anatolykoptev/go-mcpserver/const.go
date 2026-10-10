package mcpserver

// JSON schema type and key constants — eliminate goconst warnings for repeated literals.
const (
	jsonTypeBoolean = "boolean"
	jsonTypeInteger = "integer"
	jsonTypeNull    = "null"
	jsonTypeObject  = "object"
	jsonTypeArray   = "array"

	jsonKeyType        = "type"
	jsonKeyDescription = "description"
	jsonKeyContent     = "content"

	flagStdio = "--stdio"

	// TransportStdio selects stdin/stdout as the MCP transport — set it as
	// Config.Transport. The legacy --stdio os.Args scan in isStdio() remains
	// as a fallback for consumers without a flag-parsing layer.
	TransportStdio = "stdio"

	// TransportHTTP is the default HTTP transport — explicit form of "".
	TransportHTTP = "http"
)
