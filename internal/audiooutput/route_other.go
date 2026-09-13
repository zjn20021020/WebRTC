//go:build !windows

package audiooutput

func Current() Route {
	return Route{Kind: "unknown", Source: "system", Reason: "unsupported_platform"}
}
