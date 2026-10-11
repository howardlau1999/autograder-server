package main

// originPolicy decides which browser origins may make cross-origin requests
// to the server. It backs the CORS handling of the gRPC-Web bridge and of the
// file upload/download endpoints; same-origin requests never consult it.
type originPolicy struct {
	allowed map[string]bool
	devMode bool
}

func newOriginPolicy(allowedOrigins []string, devMode bool) *originPolicy {
	p := &originPolicy{allowed: make(map[string]bool, len(allowedOrigins)), devMode: devMode}
	for _, o := range allowedOrigins {
		p.allowed[o] = true
	}
	return p
}

// allowCrossOrigin reports whether a cross-origin request from origin is
// permitted. In development every origin is reflected for convenience; in
// production the origin must be on the configured whitelist.
func (p *originPolicy) allowCrossOrigin(origin string) bool {
	return p.allowed[origin] || p.devMode
}
