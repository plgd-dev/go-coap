module github.com/plgd-dev/go-coap/v3

go 1.20

require (
	github.com/dsnet/golib/memfile v1.0.0
	github.com/hashicorp/go-multierror v1.1.1
	github.com/pion/dtls/v2 v2.2.8-0.20240201071732-2597464081c8
	github.com/pion/transport/v3 v3.0.1
	github.com/stretchr/testify v1.8.4
	go.uber.org/atomic v1.11.0
	golang.org/x/exp v0.0.0-20240222234643-814bf88cf225
	golang.org/x/net v0.21.0
	golang.org/x/sync v0.6.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/pion/logging v0.2.2 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/crypto v0.19.0 // indirect
	golang.org/x/sys v0.17.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// note: github.com/pion/dtls/v2/pkg/net package is not yet available in release branches,
// so we force to the use of the pinned master branch
replace github.com/pion/dtls/v2 => github.com/pion/dtls/v2 v2.2.8-0.20240201071732-2597464081c8
