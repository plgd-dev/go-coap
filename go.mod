module github.com/plgd-dev/go-coap/v3

go 1.20

require (
	github.com/dsnet/golib/memfile v1.0.0
	github.com/pion/dtls/v2 v2.2.8-0.20240701035148-45e16a098c47
	github.com/pion/transport/v3 v3.0.7
	github.com/stretchr/testify v1.9.0
	go.uber.org/atomic v1.11.0
	golang.org/x/exp v0.0.0-20240719175910-8a7402abbf56
	golang.org/x/net v0.28.0
	golang.org/x/sync v0.8.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pion/logging v0.2.2 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/crypto v0.26.0 // indirect
	golang.org/x/sys v0.23.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// note: github.com/pion/dtls/v2/pkg/net package is not yet available in release branches,
// so we force to the use of the pinned master branch
replace github.com/pion/dtls/v2 => github.com/pion/dtls/v2 v2.2.8-0.20240701035148-45e16a098c47
