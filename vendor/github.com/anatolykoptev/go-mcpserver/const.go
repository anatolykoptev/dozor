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
)
