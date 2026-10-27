// Package permission holds Odyssey's permissions on its IAMKit resource
// (prefix "odyssey"; IAMKit enforces one prefix per resource), named
// odyssey:<entity>:<action> (odyssey:projects:write).
//
// iam/permissions.json is the single source: permission_gen.go is generated
// from it. After editing the manifest run `make gen`, then `make iam-sync` to
// push it to IAMKit (before deploying code that checks a new permission).
//
// The same catalog also gates the MCP tools: a tool is listed for a user only
// when the user holds the permission it requires (per-user tool toggles are
// IAMKit grants, not a separate Odyssey setting).
package permission

//go:generate go run ./gen -root ../..

// Role is a role on the resource, as declared in the manifest.
type Role struct {
	Name        string
	Permissions []string
}
