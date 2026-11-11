//go:build release

package access

// devBuild: a release build has no dev login, whatever the environment says.
const devBuild = false
